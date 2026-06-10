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
)

// keyframeProbe inspects buffers on a parser/depayloader src pad and:
//   - drops buffers until a keyframe has been observed
//   - throttled-requests a PLI from upstream while no keyframe is seen
//   - marks the first accepted keyframe with the DISCONT flag so downstream
//     elements reset their state on resume.
//   - restores a missing PTS (GST_CLOCK_TIME_NONE) from the last good PTS so
//     downstream stays monotonic across baseparse hiccups, and treats any
//     missing-PTS event as a parser stall worth re-requesting a keyframe for
//     (vp9parse in particular emits GST_CLOCK_TIME_NONE while waiting for a
//     keyframe).
type keyframeProbe struct {
	trackID  string
	mimeType types.MimeType

	srcPad  *gst.Pad
	sinkPad *gst.Pad // optional, attached for VP9 diagnostics only

	srcProbeID  uint64
	sinkProbeID uint64

	onRequestPLI func()

	logger logger.Logger

	keyframeSeen          atomic.Bool
	keyframePending       atomic.Bool
	lastKeyframeRequestNS atomic.Int64

	// VP9 missing-PTS diagnostics
	lastSrcPTS    atomic.Uint64
	lastSrcValid  atomic.Bool
	missingPTS    atomic.Bool
	lastSinkPTS   atomic.Uint64
	lastSinkValid atomic.Bool

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

	p.srcProbeID = srcPad.AddProbe(gst.PadProbeTypeBuffer, p.onSrcBuffer)

	// vp9parse emits buffers with GST_CLOCK_TIME_NONE when it stalls waiting
	// for a keyframe; the sink-pad probe gives us a second timeline to log
	// against and warn on backward-PTS jumps from vp9parse.
	if mimeType == types.MimeTypeVP9 {
		if sinkPad := element.GetStaticPad("sink"); sinkPad != nil {
			p.sinkPad = sinkPad
			p.sinkProbeID = sinkPad.AddProbe(gst.PadProbeTypeBuffer, p.onSinkBuffer)
		}
	}

	return p, nil
}

func (p *keyframeProbe) Close() {
	p.logKeyframeHistory("probe_closed")

	if p.srcPad != nil {
		p.srcPad.RemoveProbe(p.srcProbeID)
		p.srcPad.Unref()
		p.srcPad = nil
	}
	if p.sinkPad != nil {
		p.sinkPad.RemoveProbe(p.sinkProbeID)
		p.sinkPad.Unref()
		p.sinkPad = nil
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
		p.handleMissingPTS()
		if !p.lastSrcValid.Load() {
			return gst.PadProbeDrop
		}
		// Restore from the last good PTS so downstream stays monotonic. The
		// buffer may turn out to be a keyframe that recovers the stream — in
		// that case the keyframe branch below clears keyframePending and lets
		// it through; otherwise the keyframePending/keyframeSeen checks at the
		// bottom will drop it.
		restored := gst.ClockTime(p.lastSrcPTS.Load())
		buffer.SetPresentationTimestamp(restored)
		pts = time.Duration(uint64(restored))
	} else {
		p.lastSrcPTS.Store(uint64(pts))
		p.lastSrcValid.Store(true)
		p.missingPTS.Store(false)
	}

	isKeyframe := buffer.GetFlags()&gst.BufferFlagDeltaUnit == 0

	if isKeyframe {
		wasPending := p.keyframePending.Swap(false)
		firstKeyframe := !p.keyframeSeen.Swap(true)
		if wasPending || firstKeyframe {
			buffer.SetFlags(buffer.GetFlags() | gst.BufferFlagDiscont)
			if wasPending {
				p.logger.Debugw("keyframe pending, got one")
			}
		}
		p.trackKeyframe(pts)
		return gst.PadProbeOK
	}

	if !p.keyframeSeen.Load() {
		// pre-keyframe P-frame — drop and request a keyframe
		p.keyframePending.Store(true)
		p.requestKeyframeIfDue()
		return gst.PadProbeDrop
	}

	if p.keyframePending.Load() {
		// mid-stream stall (missing PTS observed) — drop until next keyframe
		p.requestKeyframeIfDue()
		return gst.PadProbeDrop
	}

	return gst.PadProbeOK
}

func (p *keyframeProbe) onSinkBuffer(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	buffer := info.GetBuffer()
	if buffer == nil {
		return gst.PadProbeOK
	}

	pts, ok := clockTimeToDuration(buffer.PresentationTimestamp())
	if !ok {
		return gst.PadProbeOK
	}

	if !p.lastSinkValid.Load() {
		p.lastSinkPTS.Store(uint64(pts))
		p.lastSinkValid.Store(true)
		return gst.PadProbeOK
	}

	prev := time.Duration(p.lastSinkPTS.Load())
	if delta := pts - prev; delta < 0 {
		p.logger.Warnw("parser sink pts moved backwards", nil, "delta", delta)
		p.logKeyframeHistory("backward_pts")
	}

	p.lastSinkPTS.Store(uint64(pts))
	return gst.PadProbeOK
}

func (p *keyframeProbe) handleMissingPTS() {
	p.keyframePending.Store(true)
	p.requestKeyframeIfDue()

	if !p.missingPTS.CompareAndSwap(false, true) {
		return
	}

	fields := []any{}
	if p.lastSrcValid.Load() {
		fields = append(fields, "lastValidPTS", time.Duration(p.lastSrcPTS.Load()))
	}
	if avg, count, ok := p.keyframeStats(); ok {
		fields = append(fields, "avgKeyframeInterval", avg, "keyframesTracked", count)
	}
	p.logger.Warnw("parser buffer missing PTS", nil, fields...)
	p.logKeyframeHistory("missing_pts")
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
		p.keyframePTS = p.keyframePTS[1:]
	}
}

func (p *keyframeProbe) requestKeyframeIfDue() {
	if p.onRequestPLI == nil {
		return
	}
	if !p.keyframePending.Load() {
		return
	}

	now := time.Now().UnixNano()
	last := p.lastKeyframeRequestNS.Load()
	if last != 0 && time.Duration(now-last) < keyframeRequestInterval {
		return
	}

	p.onRequestPLI()
	p.lastKeyframeRequestNS.Store(now)
}

func clockTimeToDuration(ct gst.ClockTime) (time.Duration, bool) {
	if ct == gst.ClockTimeNone {
		return 0, false
	}
	return time.Duration(uint64(ct)), true
}

func (p *keyframeProbe) keyframeStats() (time.Duration, int, bool) {
	p.keyframeMu.Lock()
	defer p.keyframeMu.Unlock()

	if p.totalIntervals == 0 {
		return 0, len(p.keyframePTS), false
	}
	return p.totalIntervalSum / time.Duration(p.totalIntervals), p.totalIntervals + 1, true
}

func (p *keyframeProbe) logKeyframeHistory(reason string) {
	p.keyframeMu.Lock()
	if len(p.keyframePTS) == 0 {
		p.keyframeMu.Unlock()
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
	p.keyframeMu.Unlock()
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
