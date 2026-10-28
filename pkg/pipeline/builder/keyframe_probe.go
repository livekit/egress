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
	"sync"
	"sync/atomic"
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
	// keyframeWatchdogInterval matches the PLI throttle in the app writer.
	keyframeWatchdogInterval = time.Second
)

// keyframeProbe gates a parser/depayloader's src pad until the first keyframe
// arrives. It exists to recover from the case where the egress subscribes
// mid-GOP (or the stream-start PLI is lost): packets keep flowing but none of
// them is decodable, and no other component would ask the publisher for a
// fresh keyframe.
//
// While waiting for the first keyframe it:
//   - drops buffers (so the decoder doesn't error on orphaned P-frames), and
//   - throttled-requests a PLI from upstream on each incoming buffer, and
//   - requests one every second from a watchdog, because a parser in front of
//     the probe discards non-keyframes and may leave it with no buffers at all.
//     The watchdog stops at the first keyframe, at EOS, or when the probe closes.
//
// After that it's mostly inert: it just patches the occasional baseparse
// missing-PTS buffer from the last good value, so downstream stays monotonic.
type keyframeProbe struct {
	trackID  string
	mimeType types.MimeType

	srcPad       *gst.Pad
	srcProbeID   uint64
	eventProbeID uint64

	onRequestPLI func()

	logger logger.Logger

	// keyframePending is shared with the watchdog goroutine.
	keyframePending atomic.Bool
	stopWatchdog    chan struct{}
	closeOnce       sync.Once

	// The probe state below is touched only from the upstream peer's streaming
	// thread (via the buffer pad probe), so no synchronization is needed.
	lastKeyframeRequest time.Time
	lastSrcPTS          time.Duration
	lastSrcValid        bool

	keyframeMu       deadlock.Mutex
	keyframePTS      []time.Duration
	totalIntervalSum time.Duration
	totalIntervals   int
}

func newKeyframeProbe(trackID string, mimeType types.MimeType, element *gst.Element, onRequestPLI func()) (*keyframeProbe, error) {
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
	p.keyframePending.Store(true)

	p.srcProbeID = srcPad.AddProbe(gst.PadProbeTypeBuffer, p.onSrcBuffer)
	p.eventProbeID = srcPad.AddProbe(gst.PadProbeTypeEventDownstream, p.onSrcEvent)
	p.startKeyframeWatchdog(keyframeWatchdogInterval)

	return p, nil
}

// startKeyframeWatchdog requests a keyframe every interval until the first one
// passes the probe or the probe is closed.
func (p *keyframeProbe) startKeyframeWatchdog(interval time.Duration) {
	if p.onRequestPLI == nil {
		return
	}
	p.stopWatchdog = make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-p.stopWatchdog:
				return
			case <-ticker.C:
				if !p.keyframePending.Load() {
					return
				}
				p.logger.Debugw("still waiting for the first keyframe, requesting one")
				p.onRequestPLI()
			}
		}
	}()
}

func (p *keyframeProbe) stopKeyframeWatchdog() {
	p.closeOnce.Do(func() {
		if p.stopWatchdog != nil {
			close(p.stopWatchdog)
		}
	})
}

func (p *keyframeProbe) Close() {
	p.stopKeyframeWatchdog()
	p.logKeyframeHistory("probe_closed")

	if p.srcPad != nil {
		p.srcPad.RemoveProbe(p.srcProbeID)
		p.srcPad.RemoveProbe(p.eventProbeID)
		p.srcPad = nil
	}
}

func (p *keyframeProbe) onSrcEvent(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	if event := info.GetEvent(); event != nil {
		p.processEvent(event)
	}
	return gst.PadProbeOK
}

// processEvent stops the watchdog at EOS: the video bin only closes probes
// for removed tracks, not when the pipeline shuts down.
func (p *keyframeProbe) processEvent(event *gst.Event) {
	if event.Type() == gst.EventTypeEOS {
		p.stopKeyframeWatchdog()
	}
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

	if isKeyframe {
		if p.keyframePending.CompareAndSwap(true, false) {
			p.logger.Debugw("keyframe pending, got one")
		}
		p.trackKeyframe(pts)
		return gst.PadProbeOK
	}

	if p.keyframePending.Load() {
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
	if !p.keyframePending.Load() {
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
