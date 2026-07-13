// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package builder

import (
	"fmt"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/linkdata/deadlock"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

const (
	keyframeHistorySize     = 10
	keyframeRequestInterval = 200 * time.Millisecond
)

// keyframeProbe gates a parser/depayloader's src pad until the first keyframe
// arrives. It exists to recover from the case where the egress subscribes
// mid-GOP (or the stream-start PLI is lost): packets keep flowing but none of
// them is decodable, and no other component would ask the publisher for a
// fresh keyframe.
//
// While waiting for the first keyframe it:
//   - drops buffers (so the decoder doesn't error on orphaned P-frames), and
//   - throttled-requests a PLI from upstream on each incoming buffer.
//
// After that it's mostly inert: it just patches the occasional baseparse
// missing-PTS buffer from the last good value, so downstream stays monotonic.
//
// In video pass-through mode there is no local encoder, so upstream
// GstForceKeyUnit events (sent by splitmuxsink at segment boundaries) would
// otherwise die unanswered. When forwardForceKeyUnit is set, the probe bridges
// them to a PLI to the publisher, reusing the same onRequestPLI callback (and
// therefore the appwriter's existing 1s PLI throttle).
type keyframeProbe struct {
	trackID  string
	mimeType types.MimeType

	srcPad       *gst.Pad
	srcProbeID   uint64
	eventProbeID uint64

	onRequestPLI func()
	injector     *freezeInjector // pass-through only, nil otherwise

	logger logger.Logger

	// All probe state below is touched only from the upstream peer's streaming
	// thread (via the buffer pad probe), so no synchronization is needed.
	keyframePending     bool
	lastKeyframeRequest time.Time
	lastSrcPTS          time.Duration
	lastSrcValid        bool

	keyframeMu       deadlock.Mutex
	keyframePTS      []time.Duration
	totalIntervalSum time.Duration
	totalIntervals   int
}

func newKeyframeProbe(trackID string, mimeType types.MimeType, element *gst.Element, onRequestPLI func(), forwardForceKeyUnit bool) (*keyframeProbe, error) {
	srcPad := element.GetStaticPad("src")
	if srcPad == nil {
		return nil, errors.ErrGstPipelineError(newMissingPadError(element.GetName(), "src"))
	}

	p := &keyframeProbe{
		trackID:      trackID,
		mimeType:     mimeType,
		srcPad:       srcPad,
		onRequestPLI: onRequestPLI,
		logger:       logger.GetLogger().WithValues("trackID", trackID, "component", "keyframe_probe", "mime", mimeType),
		keyframePTS:  make([]time.Duration, 0, keyframeHistorySize),
	}
	p.keyframePending = true

	p.srcProbeID = srcPad.AddProbe(gst.PadProbeTypeBuffer, p.onSrcBuffer)
	if forwardForceKeyUnit {
		p.eventProbeID = srcPad.AddProbe(gst.PadProbeTypeEventUpstream, p.onUpstreamEvent)
	}

	return p, nil
}

func (p *keyframeProbe) Close() {
	p.logKeyframeHistory("probe_closed")

	if p.srcPad != nil {
		p.srcPad.RemoveProbe(p.srcProbeID)
		if p.eventProbeID != 0 {
			p.srcPad.RemoveProbe(p.eventProbeID)
		}
		p.srcPad = nil
	}
}

// onUpstreamEvent bridges splitmuxsink's GstForceKeyUnit requests to a PLI to
// the publisher. onRequestPLI routes through the appwriter, whose existing 1s
// throttle applies - no additional throttling here. Both the event receipt
// (here) and the actual PLI send (appwriter) are logged with timestamps so the
// egress->SFU->publisher keyframe delay can be measured.
func (p *keyframeProbe) onUpstreamEvent(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	return p.processUpstreamEvent(info.GetEvent())
}

func (p *keyframeProbe) processUpstreamEvent(event *gst.Event) gst.PadProbeReturn {
	if event == nil || event.Type() != gst.EventTypeCustomUpstream {
		return gst.PadProbeOK
	}
	s := event.GetStructure()
	if s == nil || s.Name() != "GstForceKeyUnit" {
		return gst.PadProbeOK
	}

	fields := []any{"receivedAt", time.Now().UnixNano()}
	if runningTime, err := s.GetValue("running-time"); err == nil {
		fields = append(fields, "runningTime", runningTime)
	}
	p.logger.Infow("force key unit event received, requesting PLI", fields...)

	if p.onRequestPLI != nil {
		p.onRequestPLI()
	}
	return gst.PadProbeOK
}

func (p *keyframeProbe) onSrcBuffer(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	buffer := info.GetBuffer()
	if buffer == nil {
		return gst.PadProbeOK
	}
	return p.processBuffer(buffer)
}

func (p *keyframeProbe) processBuffer(buffer *gst.Buffer) gst.PadProbeReturn {
	pts, ptsOK := clockTimeToDuration(buffer.PresentationTimestamp())
	if !ptsOK {
		if !p.lastSrcValid {
			return gst.PadProbeDrop
		}
		// Patch a missing PTS from the last good value so downstream stays
		// monotonic. baseparse occasionally drops a PTS; the buffer content
		// itself is fine.
		restored := gst.ClockTime(uint64(p.lastSrcPTS))
		buffer.SetPresentationTimestamp(restored)
		pts = p.lastSrcPTS
		p.logger.Warnw("restored missing pts from previous buffer", nil, "pts", restored)
	} else {
		p.lastSrcPTS = pts
		p.lastSrcValid = true
	}

	isKeyframe := buffer.GetFlags()&gst.BufferFlagDeltaUnit == 0

	if p.injector != nil {
		p.injector.OnRealBuffer()
		if isKeyframe {
			p.injector.CacheKeyframe(buffer, p.srcPad)
		}
	}

	if isKeyframe {
		if p.keyframePending {
			p.keyframePending = false
			p.logger.Debugw("keyframe pending, got one")
		}
		p.trackKeyframe(pts)
		return gst.PadProbeOK
	}

	if p.keyframePending {
		// still waiting for the first keyframe — drop and PLI
		p.requestKeyframeIfDue()
		return gst.PadProbeDrop
	}

	return gst.PadProbeOK
}

func (p *keyframeProbe) trackKeyframe(pts time.Duration) {
	p.keyframeMu.Lock()
	defer p.keyframeMu.Unlock()

	if count := len(p.keyframePTS); count > 0 {
		if delta := pts - p.keyframePTS[count-1]; delta > 0 {
			p.totalIntervalSum += delta
			p.totalIntervals++
		}
	}

	p.keyframePTS = append(p.keyframePTS, pts)
	if len(p.keyframePTS) > keyframeHistorySize {
		copy(p.keyframePTS, p.keyframePTS[1:])
		p.keyframePTS = p.keyframePTS[:keyframeHistorySize]
	}
}

func (p *keyframeProbe) requestKeyframeIfDue() {
	if p.onRequestPLI == nil {
		return
	}
	if !p.keyframePending {
		return
	}

	if !p.lastKeyframeRequest.IsZero() && time.Since(p.lastKeyframeRequest) < keyframeRequestInterval {
		return
	}

	p.onRequestPLI()
	p.lastKeyframeRequest = time.Now()
}

func clockTimeToDuration(ct gst.ClockTime) (time.Duration, bool) {
	if ct == gst.ClockTimeNone {
		return 0, false
	}
	return time.Duration(uint64(ct)), true
}

func (p *keyframeProbe) logKeyframeHistory(reason string) {
	p.keyframeMu.Lock()
	defer p.keyframeMu.Unlock()
	if len(p.keyframePTS) == 0 {
		return
	}

	history := make([]time.Duration, len(p.keyframePTS))
	copy(history, p.keyframePTS)
	avg := time.Duration(0)
	count := 0
	if p.totalIntervals > 0 {
		avg = p.totalIntervalSum / time.Duration(p.totalIntervals)
		count = p.totalIntervals + 1
	}
	p.logger.Debugw("keyframe history", "reason", reason, "history", history, "avgKeyframeInterval", avg, "keyframesTracked", count)
}

type missingPadError struct {
	element string
	pad     string
}

func newMissingPadError(element, pad string) error {
	return missingPadError{element: element, pad: pad}
}

func (e missingPadError) Error() string {
	return fmt.Sprintf("missing %s pad on %s", e.pad, e.element)
}
