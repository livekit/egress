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

const keyframeHistorySize = 10

// keyframeProbe gates a parser/depayloader's src pad until the first keyframe
// arrives. It exists to recover from the case where the egress subscribes
// mid-GOP (or the stream-start PLI is lost): packets keep flowing but none of
// them is decodable, and no other component would ask the publisher for a
// fresh keyframe.
//
// On creation it asks upstream to keep requesting keyframes, and it reports the
// first keyframe that passes so those requests stop. Requests are driven by the
// caller rather than by buffers reaching the probe, because a parser in front
// of it can discard everything until a keyframe. Until then it drops buffers so
// the decoder doesn't error on orphaned P-frames.
//
// After that it's mostly inert: it just patches the occasional baseparse
// missing-PTS buffer from the last good value, so downstream stays monotonic.
//
// With a capture gate (image-only egresses with image_keyframe_decoding), the
// gate decides instead which frames reach the decoder, before and after the
// first keyframe, and the probe requests and reports one keyframe per capture.
type keyframeProbe struct {
	trackID  string
	mimeType types.MimeType

	srcPad     *gst.Pad
	srcProbeID uint64

	onKeyframeRequired func()
	onKeyframeReceived func()
	gate               *captureGate // nil unless only image outputs consume the decoded frames

	logger logger.Logger

	// All probe state below is touched only from the upstream peer's streaming
	// thread (via the buffer pad probe), so no synchronization is needed.
	keyframePending bool
	lastSrcPTS      time.Duration
	lastSrcValid    bool

	keyframeMu       deadlock.Mutex
	keyframePTS      []time.Duration
	totalIntervalSum time.Duration
	totalIntervals   int
}

// newKeyframeProbe attaches the probe to element's src pad. A non-zero
// captureInterval gates the decoder to one keyframe per interval.
func newKeyframeProbe(
	trackID string,
	mimeType types.MimeType,
	element *gst.Element,
	onKeyframeRequired func(),
	onKeyframeReceived func(),
	captureInterval time.Duration,
) (*keyframeProbe, error) {
	srcPad := element.GetStaticPad("src")
	if srcPad == nil {
		return nil, errors.ErrGstPipelineError(newMissingPadError(element.GetName(), "src"))
	}

	p := &keyframeProbe{
		trackID:            trackID,
		mimeType:           mimeType,
		srcPad:             srcPad,
		onKeyframeRequired: onKeyframeRequired,
		onKeyframeReceived: onKeyframeReceived,
		logger:             logger.GetLogger().WithValues("trackID", trackID, "component", "keyframe_probe", "mime", mimeType),
		keyframePTS:        make([]time.Duration, 0, keyframeHistorySize),
	}
	if captureInterval > 0 {
		p.gate = newCaptureGate(captureInterval)
	}
	p.keyframePending = true

	p.srcProbeID = srcPad.AddProbe(gst.PadProbeTypeBuffer, p.onSrcBuffer)
	if onKeyframeRequired != nil {
		onKeyframeRequired()
	}

	return p, nil
}

func (p *keyframeProbe) Close() {
	p.logKeyframeHistory("probe_closed")

	if p.srcPad != nil {
		p.srcPad.RemoveProbe(p.srcProbeID)
		p.srcPad = nil
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

	if p.gate != nil {
		return p.processGated(pts, isKeyframe)
	}

	if isKeyframe {
		if p.keyframePending {
			p.keyframePending = false
			p.logger.Debugw("keyframe pending, got one")
			if p.onKeyframeReceived != nil {
				p.onKeyframeReceived()
			}
		}
		p.trackKeyframe(pts)
		return gst.PadProbeOK
	}

	if p.keyframePending {
		// still waiting for the first keyframe
		return gst.PadProbeDrop
	}

	return gst.PadProbeOK
}

// processGated lets the capture gate pick the frames to decode. Each captured
// keyframe is reported, so the app writer stops repeating its request.
func (p *keyframeProbe) processGated(pts time.Duration, isKeyframe bool) gst.PadProbeReturn {
	forward, requestKeyframe := p.gate.decide(pts, isKeyframe)
	if requestKeyframe && p.onKeyframeRequired != nil {
		p.logger.Debugw("capture due without a keyframe, requesting one", "pts", pts)
		p.onKeyframeRequired()
	}
	if !forward {
		return gst.PadProbeDrop
	}
	if isKeyframe {
		p.keyframePending = false
		if p.onKeyframeReceived != nil {
			p.onKeyframeReceived()
		}
		p.trackKeyframe(pts)
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
