// Copyright 2023 LiveKit, Inc.
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
	"bytes"
	"time"

	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/protocol/logger"
)

// newEmptyGapBuffer is a compositor input's gap fill: the compositor draws nothing for it
func newEmptyGapBuffer(*gst.Caps) *gst.Buffer {
	buf := gst.NewEmptyBuffer()
	buf.SetFlags(gst.BufferFlagGap | gst.BufferFlagDroppable)
	return buf
}

// newBlackFrame is a selector input's gap fill: a black I420 frame of the caps' size, nil for any other format
func newBlackFrame(caps *gst.Caps) *gst.Buffer {
	dims, ok := dimensionsFromCaps(caps)
	if !ok {
		return nil
	}
	if format, err := caps.GetStructureAt(0).GetValue("format"); err != nil || format != "I420" {
		return nil
	}
	// the I420 plane layout of gst_video_info_set_format, in limited range
	roundUp2 := func(n int) int { return (n + 1) &^ 1 }
	roundUp4 := func(n int) int { return (n + 3) &^ 3 }
	lumaSize := roundUp4(dims.width) * roundUp2(dims.height)
	chromaSize := roundUp4(roundUp2(dims.width)/2) * (roundUp2(dims.height) / 2)
	data := append(bytes.Repeat([]byte{16}, lumaSize), bytes.Repeat([]byte{128}, 2*chromaSize)...)
	return gst.NewBufferFromBytes(data)
}

// gapFillLead puts each fill mark just before the time it hands over at, since videorate gives a tied output slot
// to the earlier buffer
const gapFillLead = time.Millisecond

// newGapFillBuffer returns a fill for the pad's current caps stamped at pts, nil when there is none
func newGapFillBuffer(pad *gst.Pad, newFill func(caps *gst.Caps) *gst.Buffer, pts time.Duration) *gst.Buffer {
	caps := pad.GetCurrentCaps()
	if caps == nil {
		return nil
	}
	buf := newFill(caps)
	if buf == nil {
		logger.Warnw("no video gap fill for these caps", nil, "caps", caps.String())
		return nil
	}
	buf.SetPresentationTimestamp(gst.ClockTime(pts))
	return buf
}

// installVideoGapFill turns GAP events at sinkPad into fill buffers from the gap's start to the next frame, which
// the videorate downstream repeats. Must be the pad's first buffer probe; startFilling treats the first frame as gap-preceded.
func installVideoGapFill(sinkPad *gst.Pad, newFill func(caps *gst.Caps) *gst.Buffer, startFilling bool) {
	var (
		filling  = startFilling // a fill is out and no frame has followed it
		chaining bool
		lastMark time.Duration
	)
	fill := func(pad *gst.Pad, pts time.Duration) bool {
		buf := newGapFillBuffer(pad, newFill, pts)
		if buf == nil {
			return false
		}
		// recursive stream lock: the marks enter the pad in order
		chaining = true
		flow := pad.Chain(buf)
		chaining = false
		if flow != gst.FlowOK {
			logger.Debugw("could not fill video gap", "flow", flow, "filled", pts)
		}
		return flow == gst.FlowOK
	}

	sinkPad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if chaining || !filling {
			return gst.PadProbeOK
		}
		filling = false
		// the fill reaches up to the frame, which may come well after the gap's end, as a mid-GOP unmute's keyframe does
		if pts := info.GetBuffer().PresentationTimestamp(); pts != gst.ClockTimeNone {
			if mark := time.Duration(pts) - gapFillLead; mark > lastMark {
				fill(pad, mark)
			}
		}
		return gst.PadProbeOK
	})

	sinkPad.AddProbe(gst.PadProbeTypeEventDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		event := info.GetEvent()
		if event == nil {
			return gst.PadProbeOK
		}
		switch event.Type() {
		case gst.EventTypeSegment:
			// videorate restarts its output timeline on every segment that differs, so one that changes nothing is dropped
			current := pad.GetStickyEvent(gst.EventTypeSegment, 0)
			if current != nil && sameSegment(current.ParseSegment(), event.ParseSegment()) {
				return gst.PadProbeDrop
			}
			return gst.PadProbeOK
		case gst.EventTypeGap:
		default:
			return gst.PadProbeOK
		}
		if pad.GetCurrentCaps() == nil {
			return gst.PadProbeOK
		}
		// a track's first GAP precedes any segment: the RTP depayloader sends its own with its first packet
		if pad.GetStickyEvent(gst.EventTypeSegment, 0) == nil {
			segment := gst.NewFormattedSegment(gst.FormatTime)
			sent := pad.SendEvent(gst.NewSegmentEvent(segment))
			segment.Free()
			if !sent {
				logger.Warnw("could not send videorate a segment for a video gap", nil)
				return gst.PadProbeOK
			}
		}
		// the end mark lets videorate output through the gap before the track's next frame arrives
		start, duration := event.ParseGap()
		marks := []time.Duration{start}
		if duration > gapFillLead {
			marks = append(marks, start+duration-gapFillLead)
		}
		for _, pts := range marks {
			if !fill(pad, pts) {
				return gst.PadProbeOK
			}
			filling, lastMark = true, pts
		}
		return gst.PadProbeDrop
	})
}
