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
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/linkdata/deadlock"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
)

const (
	selectorTestFrame      = time.Second / 30
	selectorTestTrackName  = "TR_test_0"
	selectorTestTrack2Name = "TR_test_1"
	fillerBufSize          = 1
	trackBufSize           = 2
	track2BufSize          = 3
)

// selectorHarness runs the selector path's own pad probes (both gates and the
// handover) against buffers pushed synchronously from the test goroutine, so
// probe order and the shared PTS watermark are deterministic.
type selectorHarness struct {
	t        *testing.T
	b        *VideoBin
	pipeline *gst.Pipeline
	filler   *gst.Pad
	track    *gst.Pad
	track2   *gst.Pad

	mu     deadlock.Mutex
	output []outputFrame
}

type outputFrame struct {
	track bool
	size  int64
	pts   time.Duration
}

func newSelectorHarness(t *testing.T) *selectorHarness {
	t.Helper()
	initGStreamer(t)

	conf := &config.PipelineConfig{Live: true}
	conf.Latency.PipelineLatency = 3 * time.Second
	conf.Width, conf.Height, conf.Framerate = 16, 16, 30

	holder, err := gstreamer.NewPipeline("selector_test_holder", 0, &gstreamer.Callbacks{})
	require.NoError(t, err)

	selector, err := gst.NewElement("input-selector")
	require.NoError(t, err)
	sink, err := gst.NewElement("fakesink")
	require.NoError(t, err)
	require.NoError(t, sink.SetProperty("sync", false))
	require.NoError(t, sink.SetProperty("async", false))

	pipeline, err := gst.NewPipeline("selector_test")
	require.NoError(t, err)
	require.NoError(t, pipeline.AddMany(selector, sink))
	require.NoError(t, selector.Link(sink))

	b := &VideoBin{
		bin:    holder.NewBin("video"),
		conf:   conf,
		pads:   make(map[string]*gst.Pad),
		names:  make(map[string]string),
		probes: make(map[string]*keyframeProbe),
	}
	b.selector = selector

	// the filler's elements stay unlinked in the holder; its selector pad and
	// gate are the production ones, fed by the harness instead of videotestsrc
	require.NoError(t, b.addVideoTestSrcBin())
	require.NoError(t, b.setSelectorPad(videoTestSrcName))
	require.NoError(t, b.createSrcPad("TR_test", selectorTestTrackName))
	require.NoError(t, b.createSrcPad("TR_test2", selectorTestTrack2Name))

	h := &selectorHarness{t: t, b: b, pipeline: pipeline}
	selector.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		h.mu.Lock()
		buf := info.GetBuffer()
		h.output = append(h.output, outputFrame{
			track: buf.GetSize() == trackBufSize,
			size:  buf.GetSize(),
			pts:   time.Duration(buf.PresentationTimestamp()),
		})
		h.mu.Unlock()
		return gst.PadProbeOK
	})

	require.NoError(t, pipeline.SetState(gst.StatePlaying))
	t.Cleanup(func() { _ = pipeline.SetState(gst.StateNull) })

	h.filler = h.feed("filler", b.pads[videoTestSrcName])
	h.track = h.feed("track", b.pads[selectorTestTrackName])
	h.track2 = h.feed("track2", b.pads[selectorTestTrack2Name])
	return h
}

func (h *selectorHarness) feed(name string, selectorPad *gst.Pad) *gst.Pad {
	h.t.Helper()
	src := gst.NewPad(name+"_src", gst.PadDirectionSource)
	require.True(h.t, src.SetActive(true))
	require.Equal(h.t, gst.PadLinkOK, src.Link(selectorPad))
	require.True(h.t, src.PushEvent(gst.NewStreamStartEvent(name)))
	require.True(h.t, src.PushEvent(gst.NewCapsEvent(gst.NewCapsFromString(
		"video/x-raw,format=I420,width=16,height=16,framerate=30/1"))))
	require.True(h.t, src.PushEvent(gst.NewSegmentEvent(gst.NewFormattedSegment(gst.FormatTime))))
	return src
}

func (h *selectorHarness) push(src *gst.Pad, size int64, pts time.Duration) {
	h.t.Helper()
	buf := gst.NewBufferWithSize(size)
	buf.SetPresentationTimestamp(gst.ClockTime(uint64(pts)))
	buf.SetDuration(gst.ClockTime(uint64(selectorTestFrame)))
	require.Equal(h.t, gst.FlowOK, src.Push(buf))
}

type selectorOutput struct {
	trackShown   int
	handoverGap  time.Duration // first track PTS minus last filler PTS before it
	backwardsPTS bool
}

func (h *selectorHarness) summarize() selectorOutput {
	h.mu.Lock()
	defer h.mu.Unlock()

	var out selectorOutput
	var lastFiller, prev time.Duration
	for i, f := range h.output {
		if i > 0 && f.pts < prev {
			out.backwardsPTS = true
		}
		prev = f.pts
		if !f.track {
			lastFiller = f.pts
			continue
		}
		if out.trackShown == 0 {
			out.handoverGap = f.pts - lastFiller
		}
		out.trackShown++
	}
	return out
}

// A live egress with no video shows the filler, which reaches the selector
// videoTestSrcDelay behind running time. A track subscribes and, after a
// keyframe wait, delivers decoded frames whose PTS trails running time by lag.
// The selector always reaches the track and output PTS never goes backwards.
// A track ahead of the filler leaves a gap of videoTestSrcDelay - lag; a track
// behind it leaves no gap and loses lag - videoTestSrcDelay of its frames.
func TestSelectorHandover_TrackTrailingRunningTime(t *testing.T) {
	const (
		fillerOnlyTicks  = 30 // 1s of filler before the track subscribes
		keyframeWait     = 60 // 2s from subscribe to first decoded frame
		trackTicks       = 300
		fillerLagRunTime = videoTestSrcDelay
	)

	for _, lag := range []time.Duration{
		0,
		1500 * time.Millisecond,
		2500 * time.Millisecond,
		2900 * time.Millisecond,
		4 * time.Second,
	} {
		t.Run(fmt.Sprintf("lag=%s", lag), func(t *testing.T) {
			h := newSelectorHarness(t)

			// running time starts once the filler queue has filled
			runningTime := func(tick int) time.Duration {
				return fillerLagRunTime + time.Duration(tick)*selectorTestFrame
			}

			tick := 0
			for ; tick < fillerOnlyTicks; tick++ {
				h.push(h.filler, fillerBufSize, runningTime(tick)-fillerLagRunTime)
			}

			require.NoError(t, h.b.setTrackVisible(selectorTestTrackName, true))

			for ; tick < fillerOnlyTicks+keyframeWait; tick++ {
				h.push(h.filler, fillerBufSize, runningTime(tick)-fillerLagRunTime)
			}

			firstTrackTick := tick
			for ; tick < firstTrackTick+trackTicks; tick++ {
				h.push(h.filler, fillerBufSize, runningTime(tick)-fillerLagRunTime)
				h.push(h.track, trackBufSize, runningTime(tick)-lag)
			}

			out := h.summarize()
			h.b.mu.Lock()
			selected := h.b.selectedPad
			h.b.mu.Unlock()
			t.Logf("track frames shown: %d of %d, handover gap: %s, selected pad: %s",
				out.trackShown, trackTicks, out.handoverGap, selected)

			require.Equal(t, selectorTestTrackName, selected,
				"selector never left the filler; every track frame was dropped")
			require.False(t, out.backwardsPTS, "output PTS went backwards")

			maxGap := max(fillerLagRunTime-lag, 0) + selectorTestFrame
			require.LessOrEqual(t, out.handoverGap, maxGap,
				"handover left a %s hole in the output timeline", out.handoverGap)

			maxLost := int(max(lag-fillerLagRunTime, 0)/selectorTestFrame) + 1
			require.GreaterOrEqual(t, out.trackShown, trackTicks-maxLost,
				"lost %d track frames, more than the %d the lag forces", trackTicks-out.trackShown, maxLost)
		})
	}
}

// A second video track subscribing while the first is on screen is a
// track-to-track handover. The outgoing track keeps feeding until the incoming
// one has a frame, so the timeline has no hole whatever the incoming track's
// lag; what the lag costs is wall clock, not recorded video.
func TestSelectorHandover_TrackToTrack(t *testing.T) {
	const (
		fillerOnlyTicks = 30
		keyframeWait    = 60
		trackATicks     = 90
		trackBTicks     = 150
	)

	for _, lagB := range []time.Duration{0, 500 * time.Millisecond, 2 * time.Second} {
		t.Run(fmt.Sprintf("lagB=%s", lagB), func(t *testing.T) {
			h := newSelectorHarness(t)
			runningTime := func(tick int) time.Duration {
				return videoTestSrcDelay + time.Duration(tick)*selectorTestFrame
			}

			tick := 0
			for ; tick < fillerOnlyTicks; tick++ {
				h.push(h.filler, fillerBufSize, runningTime(tick)-videoTestSrcDelay)
			}
			require.NoError(t, h.b.setTrackVisible(selectorTestTrackName, true))
			for ; tick < fillerOnlyTicks+keyframeWait; tick++ {
				h.push(h.filler, fillerBufSize, runningTime(tick)-videoTestSrcDelay)
			}

			// A is on screen and feeding
			aStart := tick
			for ; tick < aStart+trackATicks; tick++ {
				h.push(h.filler, fillerBufSize, runningTime(tick)-videoTestSrcDelay)
				h.push(h.track, trackBufSize, runningTime(tick))
			}

			// B subscribes, then waits for a keyframe before its first frame
			require.NoError(t, h.b.setTrackVisible(selectorTestTrack2Name, true))
			bSubscribe := tick
			bSubscribeTime := runningTime(bSubscribe)
			for ; tick < bSubscribe+keyframeWait; tick++ {
				h.push(h.filler, fillerBufSize, runningTime(tick)-videoTestSrcDelay)
				h.push(h.track, trackBufSize, runningTime(tick))
			}
			bStart := tick
			for ; tick < bStart+trackBTicks; tick++ {
				h.push(h.filler, fillerBufSize, runningTime(tick)-videoTestSrcDelay)
				h.push(h.track, trackBufSize, runningTime(tick))
				h.push(h.track2, track2BufSize, runningTime(tick)-lagB)
			}

			h.mu.Lock()
			var worstGap, worstAt, prev time.Duration
			var bShown int
			var backwards bool
			for i, f := range h.output {
				if i > 0 {
					if f.pts < prev {
						backwards = true
					}
					// only the second handover; the first carries the filler's residual
					if prev >= bSubscribeTime-time.Second && f.pts-prev > worstGap {
						worstGap, worstAt = f.pts-prev, prev
					}
				}
				prev = f.pts
				if f.size == track2BufSize {
					bShown++
				}
			}
			h.mu.Unlock()

			h.b.mu.Lock()
			selected := h.b.selectedPad
			h.b.mu.Unlock()

			t.Logf("B frames shown: %d of %d, gap at handover: %s at %s, selected pad: %s",
				bShown, trackBTicks, worstGap, worstAt, selected)

			require.Equal(t, selectorTestTrack2Name, selected,
				"selector never moved to the second track")
			require.False(t, backwards, "output PTS went backwards")
			require.LessOrEqual(t, worstGap, 2*selectorTestFrame,
				"handover from one track to another left a %s hole at %s", worstGap, worstAt)

			// the outgoing track has no filler lead to spend, so the incoming
			// track's lag costs it that much of its own output
			maxLost := int(lagB/selectorTestFrame) + 1
			require.GreaterOrEqual(t, bShown, trackBTicks-maxLost,
				"lost %d of the second track's frames, more than the %d its lag forces",
				trackBTicks-bShown, maxLost)
		})
	}
}
