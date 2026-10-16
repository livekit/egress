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
	"encoding/binary"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/pipeline/tempo"
)

type fakeDriftNotifier struct {
	processed   []time.Duration
	canceled    int
	tier        tempo.Tier
	onProcessed func()
}

func (f *fakeDriftNotifier) DriftProcessed(actual time.Duration) {
	f.processed = append(f.processed, actual)
	if f.onProcessed != nil {
		f.onProcessed()
	}
}
func (f *fakeDriftNotifier) CancelInFlight()  { f.canceled++ }
func (f *fakeDriftNotifier) Tier() tempo.Tier { return f.tier }

func newTestPacer() (*audioPacer, *fakeDriftNotifier, *[]float64) {
	tc := &fakeDriftNotifier{}
	tempos := &[]float64{}
	return &audioPacer{
		setTempo:            func(rate float64) { *tempos = append(*tempos, rate) },
		tc:                  tc,
		tempoAdjustmentRate: 0.05,
		boostedRate:         0.1,
	}, tc, tempos
}

// feed pushes 20ms input buffers until the correction completes and returns how many it took,
// including the buffer whose arrival completes it
func feed(p *audioPacer, max int) int {
	for i := 1; i <= max; i++ {
		p.onInput(20 * time.Millisecond)
		if !p.active.Load() {
			return i
		}
	}
	return max
}

func TestAudioPacerCorrectsTargetFromInput(t *testing.T) {
	for _, tc := range []struct {
		name     string
		drift    time.Duration
		tempo    float64
		credited int
	}{
		// 20ms × (1/0.95 − 1) ≈ 1.053ms per buffer
		{"audio ahead slows down", 12 * time.Millisecond, 0.95, 12},
		// 20ms × (1/1.05 − 1) ≈ −0.952ms per buffer
		{"audio behind speeds up", -12 * time.Millisecond, 1.05, 13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, notifier, tempos := newTestPacer()
			p.start(tc.drift)

			// the buffer after the last credited one completes the correction and is processed at tempo 1.0
			require.Equal(t, tc.credited+1, feed(p, 1000))
			require.Equal(t, []float64{tc.tempo, 1.0}, *tempos)
			require.Len(t, notifier.processed, 1)

			actual := notifier.processed[0]
			perBuffer := time.Duration(float64(20*time.Millisecond) * (1/tc.tempo - 1))
			require.Equal(t, time.Duration(tc.credited)*perBuffer, actual, "only buffers processed at the pacer's tempo are credited")
			require.GreaterOrEqual(t, actual.Abs(), tc.drift.Abs())
			require.Less(t, actual.Abs()-tc.drift.Abs(), perBuffer.Abs(), "overshoot is less than one input buffer's stretch")

			p.onInput(20 * time.Millisecond)
			require.Len(t, notifier.processed, 1, "input after completion credits nothing")
		})
	}
}

func TestAudioPacerCreditsBoostedTempo(t *testing.T) {
	p, notifier, tempos := newTestPacer()
	p.start(12 * time.Millisecond)

	// 5 buffers at 0.95 credit ≈ 5.26ms, then 0.9 credits ≈ 2.22ms per buffer, so 4 more cover 12ms
	for range 5 {
		p.onInput(20 * time.Millisecond)
	}
	p.boost()
	require.Equal(t, 5, feed(p, 1000))

	require.Equal(t, []float64{0.95, 0.9, 1.0}, *tempos)
	require.Len(t, notifier.processed, 1)
	require.InDelta(t, float64(5*20*time.Millisecond/19+4*20*time.Millisecond/9), float64(notifier.processed[0]), float64(time.Microsecond))
}

func TestAudioPacerChainedCorrectionCreditsCompletingBuffer(t *testing.T) {
	p, notifier, tempos := newTestPacer()
	rate := 0.95
	perBuffer := time.Duration(float64(20*time.Millisecond) * (1/rate - 1))

	// the controller starts the next correction from DriftProcessed, before the completing buffer is processed
	remaining := 2
	notifier.onProcessed = func() {
		if remaining > 0 {
			remaining--
			p.start(3 * time.Millisecond)
		}
	}
	p.start(3 * time.Millisecond)

	pushed := 0
	for p.active.Load() {
		p.onInput(20 * time.Millisecond)
		pushed++
	}

	require.Len(t, notifier.processed, 3)
	var total time.Duration
	for _, actual := range notifier.processed {
		require.Equal(t, 3*perBuffer, actual)
		total += actual
	}
	// every buffer but the one that completed the last correction was processed at 0.95 and credited
	require.Equal(t, time.Duration(pushed-1)*perBuffer, total)
	require.Equal(t, []float64{0.95, 1.0, 0.95, 1.0, 0.95, 1.0}, *tempos)
}

func TestAudioPacerFlushCancelsWithoutCredit(t *testing.T) {
	p, notifier, tempos := newTestPacer()
	p.start(12 * time.Millisecond)
	p.onInput(20 * time.Millisecond)

	require.True(t, p.cancelOnFlush())
	require.Equal(t, []float64{0.95, 1.0}, *tempos)

	p.onInput(20 * time.Millisecond)
	require.Empty(t, notifier.processed)

	p.start(-12 * time.Millisecond)
	require.Equal(t, 14, feed(p, 1000))
	require.Len(t, notifier.processed, 1)
	require.Less(t, notifier.processed[0], time.Duration(0), "a new correction starts from zero")
}

// pitchHarness runs appsrc → pitch → appsink with the pacer's probes on pitch. The pacer is
// armed from a sink pad probe that runs before the pacer's, as the buffer after armAfter
// arrives, so that buffer is both credited and processed at the new tempo.
type pitchHarness struct {
	t        *testing.T
	pipeline *gst.Pipeline
	src      *app.Source
	sink     *app.Sink
	pitch    *gst.Element
	pacer    *audioPacer
	notifier *fakeDriftNotifier
	seen     atomic.Int64 // input buffers that reached pitch's sink pad
	pushed   int
}

const pitchTestCaps = "audio/x-raw,format=F32LE,rate=48000,channels=2,layout=interleaved"

func newPitchHarness(t *testing.T, adjustmentRate float64, armAfter int64, drift time.Duration) *pitchHarness {
	t.Helper()
	initGStreamer(t)

	pitch, err := gst.NewElement("pitch")
	if err != nil {
		t.Skip("pitch (soundtouch) element not available")
	}
	src, err := app.NewAppSrc()
	require.NoError(t, err)
	sink, err := app.NewAppSink()
	require.NoError(t, err)
	src.SetCaps(gst.NewCapsFromString(pitchTestCaps))
	src.SetArg("format", "time")
	require.NoError(t, sink.SetProperty("sync", false))

	pipeline, err := gst.NewPipeline("")
	require.NoError(t, err)
	require.NoError(t, pipeline.AddMany(src.Element, pitch, sink.Element))
	require.NoError(t, gst.ElementLinkMany(src.Element, pitch, sink.Element))

	h := &pitchHarness{t: t, pipeline: pipeline, src: src, sink: sink, pitch: pitch, notifier: &fakeDriftNotifier{}}
	h.pacer = newAudioPacer(pitch, adjustmentRate, 2*adjustmentRate)
	h.pacer.tc = h.notifier
	pitch.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		if h.seen.Add(1) == armAfter+1 && drift != 0 {
			h.pacer.start(drift)
		}
		return gst.PadProbeOK
	})
	installPitchProbes(h.pacer)

	require.NoError(t, pipeline.SetState(gst.StatePlaying))
	t.Cleanup(func() { _ = pipeline.SetState(gst.StateNull) })
	return h
}

// push sends n 20ms buffers of a 440Hz tone and waits until pitch has received them
func (h *pitchHarness) push(n int) {
	const frames = 960
	for range n {
		data := make([]byte, frames*2*4)
		for f := range frames {
			v := float32(0.3 * math.Sin(2*math.Pi*440*float64(h.pushed*frames+f)/48000))
			binary.LittleEndian.PutUint32(data[f*8:], math.Float32bits(v))
			binary.LittleEndian.PutUint32(data[f*8+4:], math.Float32bits(v))
		}
		buf := gst.NewBufferFromBytes(data)
		buf.SetPresentationTimestamp(gst.ClockTime(time.Duration(h.pushed) * 20 * time.Millisecond))
		buf.SetDuration(gst.ClockTime(20 * time.Millisecond))
		require.Equal(h.t, gst.FlowOK, h.src.PushBuffer(buf))
		h.pushed++
	}
	require.Eventually(h.t, func() bool { return h.seen.Load() == int64(h.pushed) }, 5*time.Second, time.Millisecond)
}

// finish ends the stream and returns output minus input duration
func (h *pitchHarness) finish() time.Duration {
	h.src.EndStream()
	var out time.Duration
	for {
		sample := h.sink.PullSample()
		if sample == nil {
			break
		}
		if d := sample.GetBuffer().Duration(); d != gst.ClockTimeNone {
			out += *d.AsDuration()
		}
	}
	return out - time.Duration(h.pushed)*20*time.Millisecond
}

func TestAudioPacerPitchAppliesCreditedStretch(t *testing.T) {
	t.Run("no correction", func(t *testing.T) {
		h := newPitchHarness(t, 0.05, 0, 0)
		h.push(150)
		require.InDelta(t, 0, float64(h.finish()), float64(100*time.Microsecond), "pitch at tempo 1.0 adds nothing")
	})

	for _, tc := range []struct {
		name  string
		rate  float64
		drift time.Duration
	}{
		{"slow down", 0.05, 12 * time.Millisecond},
		{"speed up", 0.05, -12 * time.Millisecond},
		{"slow down at boosted rate", 0.1, 12 * time.Millisecond},
		{"speed up at boosted rate", 0.1, -12 * time.Millisecond},
		{"long correction", 0.05, 200 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// armed mid-stream, with pitch holding audio from the warm-up
			h := newPitchHarness(t, tc.rate, 53, tc.drift)
			h.push(453)
			applied := h.finish()

			require.Len(t, h.notifier.processed, 1)
			require.InDelta(t, float64(h.notifier.processed[0]), float64(applied), float64(100*time.Microsecond),
				"the stretch pitch applied matches what the pacer reported")
			require.GreaterOrEqual(t, applied.Abs(), tc.drift.Abs()-100*time.Microsecond)
		})
	}
}

func TestAudioPacerPitchFlushCancelsCorrection(t *testing.T) {
	h := newPitchHarness(t, 0.05, 20, 200*time.Millisecond)
	h.push(30)
	require.True(t, h.pacer.active.Load())

	srcPad := h.src.GetStaticPad("src")
	require.True(t, srcPad.PushEvent(gst.NewFlushStartEvent()))
	require.True(t, srcPad.PushEvent(gst.NewFlushStopEvent(false)))

	require.False(t, h.pacer.active.Load(), "a flush reaching pitch cancels the correction")
	require.Equal(t, 1, h.notifier.canceled)
	require.Empty(t, h.notifier.processed)
}
