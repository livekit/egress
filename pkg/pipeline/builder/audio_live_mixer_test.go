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
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/pipeline/tempo"
)

// TestLiveMixerKeepsCorrectedTrack feeds a live audiomixer in real time from a source whose
// sample clock is 3% off, timestamped on arrival, through pitch and the pacer. The source's
// drift passes the mixer latency after about 92s, and the mixer drops a track's buffers once
// they are that late, so the drops show whether the track stayed on the pipeline clock.
func TestLiveMixerKeepsCorrectedTrack(t *testing.T) {
	if os.Getenv("LIVE_MIXER_TEST") == "" {
		t.Skip("runs in real time for 150s; set LIVE_MIXER_TEST=1")
	}
	initGStreamer(t)

	const skew = 0.03
	for _, tc := range []struct {
		name string
		// positive: the source's clock is slow and its audio arrives late; negative: fast
		skew float64
		// the tempo controller is told the drift each second
		corrected bool
	}{
		{"slow clock, corrected", skew, true},
		{"fast clock, corrected", -skew, true},
		{"slow clock, not corrected", skew, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			drops := liveMixerDrops(t, tc.skew, tc.corrected)
			if tc.corrected {
				require.Zero(t, drops, "the mixer dropped buffers of a corrected track")
			} else {
				require.Positive(t, drops, "the mixer keeps a track that drifted past its latency")
			}
		})
	}
}

// liveMixerDrops runs the skewed source into the mixer for 150s and returns how many of the
// track's buffers the mixer dropped
func liveMixerDrops(t *testing.T, skew float64, corrected bool) int64 {
	const (
		runFor       = 150 * time.Second
		mixerLatency = 2750 * time.Millisecond
	)
	require.Greater(t, time.Duration(float64(runFor)*skew).Abs(), mixerLatency, "the run takes the drift past the mixer latency")

	pitch, err := gst.NewElement("pitch")
	if err != nil {
		t.Skip("pitch (soundtouch) element not available")
	}
	src, err := app.NewAppSrc()
	require.NoError(t, err)
	src.SetCaps(gst.NewCapsFromString(pitchTestCaps))
	src.SetArg("format", "time")
	require.NoError(t, src.SetProperty("is-live", true))

	mixer, err := gst.NewElement("audiomixer")
	require.NoError(t, err)
	require.NoError(t, mixer.SetProperty("latency", uint64(mixerLatency)))
	require.NoError(t, mixer.SetProperty("alignment-threshold", uint64(3*time.Second)))

	// a live silent track keeps the mixer producing on the pipeline clock
	silence, err := gst.NewElement("audiotestsrc")
	require.NoError(t, err)
	require.NoError(t, silence.SetProperty("is-live", true))
	silence.SetArg("wave", "silence")
	silenceCaps, err := gst.NewElement("capsfilter")
	require.NoError(t, err)
	require.NoError(t, silenceCaps.SetProperty("caps", gst.NewCapsFromString(pitchTestCaps)))
	outCaps, err := gst.NewElement("capsfilter")
	require.NoError(t, err)
	require.NoError(t, outCaps.SetProperty("caps", gst.NewCapsFromString(pitchTestCaps)))
	sink, err := gst.NewElement("fakesink")
	require.NoError(t, err)
	require.NoError(t, sink.SetProperty("sync", false))
	require.NoError(t, sink.SetProperty("async", false))

	pipeline, err := gst.NewPipeline("")
	require.NoError(t, err)
	require.NoError(t, pipeline.AddMany(src.Element, pitch, mixer, silence, silenceCaps, outCaps, sink))
	require.NoError(t, gst.ElementLinkMany(src.Element, pitch))
	trackPad := mixer.GetRequestPad("sink_%u")
	require.NotNil(t, trackPad)
	require.NoError(t, trackPad.SetProperty("qos-messages", true))
	require.Equal(t, gst.PadLinkOK, pitch.GetStaticPad("src").Link(trackPad))
	require.NoError(t, gst.ElementLinkMany(silence, silenceCaps, mixer))
	require.NoError(t, gst.ElementLinkMany(mixer, outCaps, sink))

	ctrl := tempo.NewController(mixerLatency)
	pacer := newAudioPacer(pitch, 0.05, 0.1)
	pacer.tc = ctrl
	ctrl.OnTierChange(func(tier tempo.Tier) {
		if tier == tempo.TierNormal {
			pacer.brake()
		} else {
			pacer.boost()
		}
	})
	ctrl.OnDriftDetectedCallback(pacer.start)
	installPitchProbes(pacer)

	require.NoError(t, pipeline.SetState(gst.StatePlaying))
	t.Cleanup(func() { _ = pipeline.SetState(gst.StateNull) })

	var clock *gst.Clock
	require.Eventually(t, func() bool {
		clock = pipeline.GetPipelineClock()
		return clock != nil && pipeline.GetCurrentState() == gst.StatePlaying
	}, 10*time.Second, 10*time.Millisecond)
	base := pipeline.GetBaseTime()

	// the mixer posts a QoS message for each buffer it drops from the track
	var drops atomic.Int64
	done := make(chan struct{})
	busDone := make(chan struct{})
	go func() {
		defer close(busDone)
		bus := pipeline.GetPipelineBus()
		for {
			select {
			case <-done:
				return
			default:
			}
			msg := bus.TimedPop(gst.ClockTime(100 * time.Millisecond))
			if msg == nil {
				continue
			}
			switch msg.Type() {
			case gst.MessageQoS:
				drops.Add(1)
			case gst.MessageError:
				t.Errorf("pipeline error: %v", msg.ParseError())
			}
		}
	}()

	// a 20ms buffer every 20ms of the source's clock, stamped with the time it arrives
	started := time.Now()
	period := time.Duration(float64(20*time.Millisecond) * (1 + skew))
	for n := 0; ; n++ {
		if wait := time.Until(started.Add(time.Duration(n) * period)); wait > 0 {
			time.Sleep(wait)
		}
		elapsed := time.Since(started)
		if elapsed >= runFor {
			break
		}

		buf := toneBuffer(n)
		buf.SetPresentationTimestamp(gst.ClockTime(time.Duration(clock.GetTime() - base)))
		require.Equal(t, gst.FlowOK, src.PushBuffer(buf))

		// real time passed minus audio sent, as sender reports measure it
		if corrected && n%50 == 49 {
			ctrl.SetDrift(elapsed - time.Duration(n+1)*20*time.Millisecond)
		}
	}
	close(done)
	<-busDone

	return drops.Load()
}
