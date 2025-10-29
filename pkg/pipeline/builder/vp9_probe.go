package builder

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/linkdata/deadlock"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
)

const (
	keyframeHistorySize     = 10
	keyframeRequestInterval = 200 * time.Millisecond
)

// vp9ParseProbe inspects buffers around vp9parse to detect and signal missing
// PTS and capture timing diagnostics. It never mutates the media flow; state
// such as lastSinkPTS is tracked solely for logging and debugging.
type vp9ParseProbe struct {
	trackID string

	srcPad  *gst.Pad
	sinkPad *gst.Pad

	srcProbeID  uint64
	sinkProbeID uint64

	onSignal func()

	logger logger.Logger

	lastSrcPTS   atomic.Uint64
	lastSrcValid atomic.Bool
	missingPTS   atomic.Bool

	lastSinkPTS   atomic.Uint64
	lastSinkValid atomic.Bool

	keyframeMu            deadlock.Mutex
	keyframePTS           []time.Duration
	totalIntervalSum      time.Duration
	totalIntervals        int
	lastKeyframeRequestNS atomic.Int64
	keyframePending       atomic.Bool
}

func newVP9ParseProbe(trackID string, parse *gst.Element, onSignal func()) (*vp9ParseProbe, error) {
	srcPad := parse.GetStaticPad("src")
	if srcPad == nil {
		return nil, errors.ErrGstPipelineError(newMissingPadError("vp9parse", "src"))
	}

	sinkPad := parse.GetStaticPad("sink")
	if sinkPad == nil {
		srcPad.Unref()
		return nil, errors.ErrGstPipelineError(newMissingPadError("vp9parse", "sink"))
	}

	p := &vp9ParseProbe{
		trackID:     trackID,
		srcPad:      srcPad,
		sinkPad:     sinkPad,
		onSignal:    onSignal,
		logger:      logger.GetLogger().WithValues("trackID", trackID, "component", "vp9_probe"),
		keyframePTS: make([]time.Duration, 0, keyframeHistorySize),
	}

	p.srcProbeID = srcPad.AddProbe(gst.PadProbeTypeBuffer, p.onSrcBuffer)
	p.sinkProbeID = sinkPad.AddProbe(gst.PadProbeTypeBuffer, p.onSinkBuffer)

	return p, nil
}

func (p *vp9ParseProbe) Close() {
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

func (p *vp9ParseProbe) onSrcBuffer(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	buffer := info.GetBuffer()
	if buffer == nil {
		return gst.PadProbeOK
	}

	pts, ok := clockTimeToDuration(buffer.PresentationTimestamp())
	if !ok {
		p.handleMissingPTS()
		return gst.PadProbeDrop
	}

	p.handleValidPTS(buffer, pts)
	if p.keyframePending.Load() {
		return gst.PadProbeDrop
	}
	return gst.PadProbeOK
}

// just for logging purposes
func (p *vp9ParseProbe) onSinkBuffer(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
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
	delta := pts - prev
	if delta < 0 {
		p.logger.Warnw("vp9parse sink pts moved backwards", nil, "delta", delta)
		p.logKeyframeHistory("backward_pts")
	}

	p.lastSinkPTS.Store(uint64(pts))

	return gst.PadProbeOK
}

func (p *vp9ParseProbe) handleMissingPTS() {
	p.keyframePending.Store(true)
	p.requestKeyframeIfDue()

	if !p.missingPTS.CompareAndSwap(false, true) {
		return
	}

	fields := []any{}
	if p.lastSrcValid.Load() {
		last := time.Duration(p.lastSrcPTS.Load())
		fields = append(fields, "lastValidPTS", last)
	}
	if avg, count, ok := p.keyframeStats(); ok {
		fields = append(fields, "avgKeyframeInterval", avg, "keyframesTracked", count)
	}
	p.logger.Warnw("vp9parse buffer missing PTS", nil, fields...)
	p.logKeyframeHistory("missing_pts")
}

func (p *vp9ParseProbe) handleValidPTS(buffer *gst.Buffer, pts time.Duration) {
	p.lastSrcPTS.Store(uint64(pts))
	p.lastSrcValid.Store(true)
	p.missingPTS.Store(false)

	if buffer.GetFlags()&gst.BufferFlagDeltaUnit == 0 {
		wasPending := p.keyframePending.Swap(false)
		if wasPending {
			p.logger.Debugw("keyframe pending, got one")
			buffer.SetFlags(buffer.GetFlags() | gst.BufferFlagDiscont)
		}
		p.trackKeyframe(pts)
	} else {
		p.requestKeyframeIfDue()
	}
}

func (p *vp9ParseProbe) trackKeyframe(pts time.Duration) {
	p.keyframeMu.Lock()
	defer p.keyframeMu.Unlock()

	if count := len(p.keyframePTS); count > 0 {
		delta := pts - p.keyframePTS[count-1]
		if delta > 0 {
			p.totalIntervalSum += delta
			p.totalIntervals++
		}
	}

	p.keyframePTS = append(p.keyframePTS, pts)
	if len(p.keyframePTS) > keyframeHistorySize {
		p.keyframePTS = p.keyframePTS[1:]
		// sliding window only keeps the most recent timestamps for debugging logs
	}

}

func (p *vp9ParseProbe) requestKeyframeIfDue() {
	if p.onSignal == nil {
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

	p.onSignal()

	p.lastKeyframeRequestNS.Store(now)
}

func clockTimeToDuration(ct gst.ClockTime) (time.Duration, bool) {
	if ct == gst.ClockTimeNone {
		return 0, false
	}
	return time.Duration(uint64(ct)), true
}

func (p *vp9ParseProbe) keyframeStats() (time.Duration, int, bool) {
	p.keyframeMu.Lock()
	defer p.keyframeMu.Unlock()

	if p.totalIntervals == 0 {
		return 0, len(p.keyframePTS), false
	}

	avg := p.totalIntervalSum / time.Duration(p.totalIntervals)
	return avg, p.totalIntervals + 1, true
}

func (p *vp9ParseProbe) logKeyframeHistory(reason string) {
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
	p.logger.Debugw("vp9 keyframe history", "reason", reason, "history", history, "avgKeyframeInterval", avg, "keyframesTracked", count)
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
