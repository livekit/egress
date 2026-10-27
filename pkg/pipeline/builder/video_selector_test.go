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
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/linkdata/deadlock"
	"github.com/stretchr/testify/assert"
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

	// encoderQueue sits between the selector and the sink when requested.
	encoderQueue *gst.Element

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
	return buildSelectorHarness(t, false)
}

// newEncoderQueueHarness adds the production video_encoder_queue behind the
// selector, so the queue's own handling of EOS, queries and STREAM_START runs.
func newEncoderQueueHarness(t *testing.T) *selectorHarness {
	t.Helper()
	return buildSelectorHarness(t, true)
}

func buildSelectorHarness(t *testing.T, withEncoderQueue bool) *selectorHarness {
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
	var encoderQueue *gst.Element
	if withEncoderQueue {
		encoderQueue, err = gstreamer.BuildQueue("video_encoder_queue", conf.Latency.PipelineLatency, false)
		require.NoError(t, err)
		require.NoError(t, pipeline.Add(encoderQueue))
		require.NoError(t, selector.Link(encoderQueue))
		require.NoError(t, encoderQueue.Link(sink))
	} else {
		require.NoError(t, selector.Link(sink))
	}

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
	// the unlinked queue never signals, and the harness feeds the filler as if it had filled
	b.onFillerQueueLevel(uint64(videoTestSrcDelay))
	require.NoError(t, b.setSelectorPad(videoTestSrcName))
	require.NoError(t, b.createSrcPad("TR_test", selectorTestTrackName))
	require.NoError(t, b.createSrcPad("TR_test2", selectorTestTrack2Name))

	h := &selectorHarness{t: t, b: b, pipeline: pipeline, encoderQueue: encoderQueue}
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
	// with group ids on every input, as in production, the selector forwards the
	// new input's stream-start on each switch
	streamStart := gst.NewStreamStartEvent(name)
	streamStart.SetGroupID(gst.NextGroupID())
	require.True(h.t, src.PushEvent(streamStart))
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

// The filler queue releases its first buffer before min-threshold-time when a
// serialized query lands behind it. A track that arrives before the queue fills
// must still be the first thing out of the selector, or videorate anchors on the
// early filler frame and holds a frame across the gap.
func TestSelectorHandover_FillerReleasedBeforeThreshold(t *testing.T) {
	h := newSelectorHarness(t)
	h.b.fillerReady.Store(false)

	h.push(h.filler, fillerBufSize, 0)
	require.NoError(t, h.b.setTrackVisible(selectorTestTrackName, true))
	for tick := range 30 {
		h.push(h.track, trackBufSize, time.Second+time.Duration(tick)*selectorTestFrame)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	require.NotEmpty(t, h.output, "no buffer reached the selector output")
	require.True(t, h.output[0].track,
		"filler frame at %s left the selector before the track, released ahead of the queue threshold", h.output[0].pts)
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

// fillerGateThreshold caps the gate at the queue's max-size-time, which a full
// queue stops exceeding.
func TestFillerGateThreshold(t *testing.T) {
	for _, tc := range []struct {
		name        string
		maxSizeTime time.Duration
		want        time.Duration
	}{
		{"a longer max-size-time leaves the delay", 3 * time.Second, videoTestSrcDelay},
		{"a shorter max-size-time caps the delay", time.Second, time.Second},
		{"an unlimited queue leaves the delay", 0, videoTestSrcDelay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, uint64(tc.want), fillerGateThreshold(uint64(tc.maxSizeTime)))
		})
	}
}

// onFillerQueueLevel latches the gate once, and only once the queue has
// reached the resolved threshold.
func TestFillerGate_Latch(t *testing.T) {
	newBin := func() *VideoBin {
		return &VideoBin{fillerThreshold: uint64(videoTestSrcDelay)}
	}

	for _, tc := range []struct {
		name  string
		level time.Duration
		ready bool
	}{
		{"below the threshold stays closed", videoTestSrcDelay - time.Millisecond, false},
		{"at the threshold opens", videoTestSrcDelay, true},
		{"above the threshold opens", videoTestSrcDelay + time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBin()
			b.onFillerQueueLevel(uint64(tc.level))
			require.Equal(t, tc.ready, b.fillerReady.Load())
		})
	}

	t.Run("a later drain does not close the gate", func(t *testing.T) {
		b := newBin()
		b.onFillerQueueLevel(uint64(videoTestSrcDelay))
		b.onFillerQueueLevel(0)
		require.True(t, b.fillerReady.Load())
	})
}

// The production queue's max-size-time comes from the pipeline latency, so the
// bin resolves its own threshold when it is built.
func TestFillerGate_ThresholdFromQueue(t *testing.T) {
	h := newSelectorHarness(t)
	require.Equal(t, uint64(videoTestSrcDelay), h.b.fillerThreshold)
}

// Every phase of an egress ending while a track is pending, against the
// production gates, a real input-selector and the production
// video_encoder_queue:
//  1. the filler is on screen
//  2. a track comes back and is pending
//  3. the egress ends: markEnding runs, then the filler's EOS leaves through the
//     selector and is held past the encoder queue, as while the encoder drains it
//  4. the track's first frame arrives, behind the filler's last PTS
//  5. the track's ALLOCATION query is sent, the drain finishes, and the query's
//     sender frees it
//  6. later track frames arrive
//
// The track must not take the selector or have a frame leave it, no query may
// be queued behind that EOS, and the encoder queue must keep accepting data. A
// handover taken in step 4 queues the step 5 query behind the EOS; the
// STREAM_START from step 6 then restarts the queue onto the freed query
// ("refcounting problem?"), and every later push fails.
func TestSelectorHandover_EncoderQueueSurvivesEnding(t *testing.T) {
	h := newEncoderQueueHarness(t)

	eosHeld := make(chan struct{})
	release := make(chan struct{})
	var heldOnce, releaseOnce sync.Once
	releaseEOS := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseEOS)
	h.encoderQueue.GetStaticPad("src").AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if info.GetEvent().Type() == gst.EventTypeEOS {
			heldOnce.Do(func() {
				close(eosHeld)
				<-release
			})
		}
		return gst.PadProbeOK
	})

	pushTrack := func(pts time.Duration) gst.FlowReturn {
		buf := gst.NewBufferWithSize(trackBufSize)
		buf.SetPresentationTimestamp(gst.ClockTime(uint64(pts)))
		buf.SetDuration(gst.ClockTime(uint64(selectorTestFrame)))
		return h.track.Push(buf)
	}

	// 1
	const fillerTicks = 30
	for tick := 0; tick < fillerTicks; tick++ {
		h.push(h.filler, fillerBufSize, time.Duration(tick)*selectorTestFrame)
	}

	// 2
	require.NoError(t, h.b.setTrackVisible(selectorTestTrackName, true))

	// 3: the filler's pad waits in the selector after its EOS until every input
	// has EOS, so it is pushed from its own goroutine
	require.True(t, h.b.markEnding())
	go h.filler.PushEvent(gst.NewEOSEvent())
	select {
	case <-eosHeld:
	case <-time.After(5 * time.Second):
		t.Fatal("the filler's EOS never reached the encoder queue's output")
	}

	// 4
	pushTrack(10 * selectorTestFrame)

	// 5: a query queued behind the held EOS blocks its sender until the drain ends
	query := gst.NewAllocationQuery(gst.NewCapsFromString(
		"video/x-raw,format=I420,width=16,height=16,framerate=30/1"), true)
	answered := make(chan bool, 1)
	go func() { answered <- h.track.PeerQuery(query) }()
	queuedBehindEOS := false
	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		queuedBehindEOS = true
	}
	releaseEOS()
	if queuedBehindEOS {
		select {
		case <-answered:
		case <-time.After(5 * time.Second):
			t.Fatal("the ALLOCATION query's sender never woke after the drain")
		}
	}
	// the sender owns the query and frees it once answered
	runtime.SetFinalizer(query, nil)
	query.Unref()

	// 6
	var failed []gst.FlowReturn
	for i := 0; i < 10; i++ {
		if flow := pushTrack(time.Duration(fillerTicks+10+i) * selectorTestFrame); flow != gst.FlowOK {
			failed = append(failed, flow)
		}
		time.Sleep(20 * time.Millisecond)
	}

	h.b.mu.Lock()
	selected := h.b.selectedPad
	h.b.mu.Unlock()
	assert.Equal(t, videoTestSrcName, selected,
		"the track took the selector after the egress began ending")
	assert.Zero(t, h.summarize().trackShown,
		"track frames left the selector after the filler's EOS")
	assert.False(t, queuedBehindEOS,
		"the track's ALLOCATION query was queued behind the filler's EOS in video_encoder_queue")
	assert.Empty(t, failed,
		"pushes into video_encoder_queue failed after the egress began ending")
}

// A selected track's own EOS while the egress runs, as when its appsrc stops on
// a negotiation error, against the production gates, a real input-selector and
// the production video_encoder_queue:
//  1. the filler is on screen
//  2. the track takes the selector
//  3. the track's EOS arrives at the selector; if it is forwarded, it is held
//     past the encoder queue, as while the encoder drains it
//  4. the track's removal switches to the filler
//  5. the filler's ALLOCATION query is sent, the drain finishes, and the query's
//     sender frees it
//  6. later filler frames arrive
//  7. the track's removal releases its selector pad
//
// The track's EOS must not reach the encoder queue, no query may be queued
// behind it, the filler must keep the output going, and the track's EOS must
// return once its pad is released.
func TestSelectorHandover_SelectedTrackEOSWhileRunning(t *testing.T) {
	h := newEncoderQueueHarness(t)

	eosHeld := make(chan struct{})
	release := make(chan struct{})
	var heldOnce, releaseOnce sync.Once
	releaseEOS := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseEOS)
	h.encoderQueue.GetStaticPad("src").AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if info.GetEvent().Type() == gst.EventTypeEOS {
			heldOnce.Do(func() {
				close(eosHeld)
				<-release
			})
		}
		return gst.PadProbeOK
	})

	pushFiller := func(pts time.Duration) gst.FlowReturn {
		buf := gst.NewBufferWithSize(fillerBufSize)
		buf.SetPresentationTimestamp(gst.ClockTime(uint64(pts)))
		buf.SetDuration(gst.ClockTime(uint64(selectorTestFrame)))
		return h.filler.Push(buf)
	}

	// 1
	for tick := 0; tick < 30; tick++ {
		h.push(h.filler, fillerBufSize, time.Duration(tick)*selectorTestFrame)
	}

	// 2
	require.NoError(t, h.b.setTrackVisible(selectorTestTrackName, true))
	for tick := 30; tick < 40; tick++ {
		h.push(h.track, trackBufSize, time.Duration(tick)*selectorTestFrame)
	}
	h.b.mu.Lock()
	require.Equal(t, selectorTestTrackName, h.b.selectedPad)
	h.b.mu.Unlock()

	// 3: the selector holds an input's EOS until every input has one or its pad
	// is released, so it is pushed from its own goroutine
	trackEOSDone := make(chan struct{})
	go func() {
		h.track.PushEvent(gst.NewEOSEvent())
		close(trackEOSDone)
	}()
	forwarded := false
	select {
	case <-eosHeld:
		forwarded = true
	case <-time.After(time.Second):
	}

	// 4
	h.b.mu.Lock()
	trackPad := h.b.pads[selectorTestTrackName]
	h.b.mu.Unlock()
	require.NoError(t, h.b.setTrackVisible(selectorTestTrackName, false))

	// 5: a query queued behind the held EOS blocks its sender until the drain ends
	query := gst.NewAllocationQuery(gst.NewCapsFromString(
		"video/x-raw,format=I420,width=16,height=16,framerate=30/1"), true)
	answered := make(chan bool, 1)
	go func() { answered <- h.filler.PeerQuery(query) }()
	queuedBehindEOS := false
	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		queuedBehindEOS = true
	}
	releaseEOS()
	if queuedBehindEOS {
		select {
		case <-answered:
		case <-time.After(5 * time.Second):
			t.Fatal("the ALLOCATION query's sender never woke after the drain")
		}
	}
	// the sender owns the query and frees it once answered
	runtime.SetFinalizer(query, nil)
	query.Unref()

	// 6
	const fillerAfter = 10
	var failed []gst.FlowReturn
	for i := 0; i < fillerAfter; i++ {
		if flow := pushFiller(time.Duration(40+i) * selectorTestFrame); flow != gst.FlowOK {
			failed = append(failed, flow)
		}
		time.Sleep(20 * time.Millisecond)
	}

	h.mu.Lock()
	fillerShown := 0
	for _, f := range h.output {
		if !f.track && f.pts >= 40*selectorTestFrame {
			fillerShown++
		}
	}
	h.mu.Unlock()

	assert.False(t, forwarded,
		"the selected track's EOS reached video_encoder_queue while the egress was running")
	assert.False(t, queuedBehindEOS,
		"the filler's ALLOCATION query was queued behind the track's EOS in video_encoder_queue")
	assert.Empty(t, failed,
		"pushes into video_encoder_queue failed after the track's EOS")
	assert.Equal(t, fillerAfter, fillerShown,
		"filler frames did not leave the selector after the track's EOS")

	// 7
	h.b.selector.ReleaseRequestPad(trackPad)
	select {
	case <-trackEOSDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the track's EOS stayed parked in the selector after its pad was released")
	}
}

// A track composite egress never removes its tracks, so a selected video
// track's EOS while the egress runs is followed by no switch of its own:
//  1. the filler is on screen
//  2. the track takes the selector
//  3. the track's EOS arrives
//  4. later filler frames arrive
//  5. the egress ends: the other track's writer ends its stream, markEnding
//     runs, then the filler's EOS
//
// The track's EOS must not leave the selector while the egress runs, the
// filler must keep the output going, and at the end one EOS must leave the
// selector and every input's EOS must return.
func TestSelectorHandover_SelectedTrackEOSWithoutRemoval(t *testing.T) {
	h := newSelectorHarness(t)

	var eosOut atomic.Int32
	h.b.selector.GetStaticPad("src").AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if info.GetEvent().Type() == gst.EventTypeEOS {
			eosOut.Add(1)
		}
		return gst.PadProbeOK
	})

	pushEOS := func(src *gst.Pad) <-chan struct{} {
		done := make(chan struct{})
		go func() {
			src.PushEvent(gst.NewEOSEvent())
			close(done)
		}()
		return done
	}
	waitFor := func(done <-chan struct{}, msg string) {
		t.Helper()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal(msg)
		}
	}

	// 1
	for tick := 0; tick < 30; tick++ {
		h.push(h.filler, fillerBufSize, time.Duration(tick)*selectorTestFrame)
	}

	// 2
	require.NoError(t, h.b.setTrackVisible(selectorTestTrackName, true))
	for tick := 30; tick < 40; tick++ {
		h.push(h.track, trackBufSize, time.Duration(tick)*selectorTestFrame)
	}

	// 3
	// the EOS either switches the selector to the filler or leaves the selector
	trackEOSDone := pushEOS(h.track)
	var selected string
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		h.b.mu.Lock()
		selected = h.b.selectedPad
		h.b.mu.Unlock()
		if selected != selectorTestTrackName || eosOut.Load() > 0 {
			break
		}
	}

	// 4
	const fillerAfter = 10
	for i := 0; i < fillerAfter; i++ {
		buf := gst.NewBufferWithSize(fillerBufSize)
		buf.SetPresentationTimestamp(gst.ClockTime(uint64(time.Duration(40+i) * selectorTestFrame)))
		buf.SetDuration(gst.ClockTime(uint64(selectorTestFrame)))
		h.filler.Push(buf)
	}
	h.mu.Lock()
	fillerShown := 0
	for _, f := range h.output {
		if !f.track && f.pts >= 40*selectorTestFrame {
			fillerShown++
		}
	}
	h.mu.Unlock()
	eosWhileRunning := eosOut.Load()

	assert.Equal(t, videoTestSrcName, selected,
		"the selector stayed on the track after its EOS")
	assert.Zero(t, eosWhileRunning,
		"the track's EOS left the selector while the egress was running")
	assert.Equal(t, fillerAfter, fillerShown,
		"filler frames did not leave the selector after the track's EOS")

	// 5: the selector holds each input's EOS until every input has one
	_, ok := h.b.bin.UpgradeState(gstreamer.StateEOS)
	require.True(t, ok)
	track2EOSDone := pushEOS(h.track2)
	require.True(t, h.b.markEnding())
	fillerEOSDone := pushEOS(h.filler)
	waitFor(fillerEOSDone, "the filler's EOS did not return at the end of the egress")
	waitFor(track2EOSDone, "the other track's EOS stayed parked in the selector at the end of the egress")
	waitFor(trackEOSDone, "the track's EOS stayed parked in the selector at the end of the egress")
	assert.Equal(t, int32(1), eosOut.Load(),
		"one EOS should leave the selector at the end of the egress")
}

// Once the egress is ending, a selected track's EOS is part of shutdown: it
// must leave the selector and the track must stay selected.
func TestSelectorHandover_SelectedTrackEOSAtShutdown(t *testing.T) {
	h := newSelectorHarness(t)

	eosOut := make(chan struct{})
	var eosOnce sync.Once
	h.b.selector.GetStaticPad("src").AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if info.GetEvent().Type() == gst.EventTypeEOS {
			eosOnce.Do(func() { close(eosOut) })
		}
		return gst.PadProbeOK
	})

	for tick := 0; tick < 30; tick++ {
		h.push(h.filler, fillerBufSize, time.Duration(tick)*selectorTestFrame)
	}
	require.NoError(t, h.b.setTrackVisible(selectorTestTrackName, true))
	for tick := 30; tick < 40; tick++ {
		h.push(h.track, trackBufSize, time.Duration(tick)*selectorTestFrame)
	}

	_, ok := h.b.bin.UpgradeState(gstreamer.StateEOS)
	require.True(t, ok)
	go h.track.PushEvent(gst.NewEOSEvent())

	select {
	case <-eosOut:
	case <-time.After(5 * time.Second):
		t.Fatal("the selected track's EOS did not leave the selector during shutdown")
	}
	h.b.mu.Lock()
	selected := h.b.selectedPad
	h.b.mu.Unlock()
	require.Equal(t, selectorTestTrackName, selected,
		"the selector left the track on its shutdown EOS")
}
