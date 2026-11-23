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
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/rtp"
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
func (f *fakeDriftNotifier) CancelInFlight()                  { f.canceled++ }
func (f *fakeDriftNotifier) Tier() tempo.Tier                 { return f.tier }
func (f *fakeDriftNotifier) ObserveOutputShift(time.Duration) {}
func (f *fakeDriftNotifier) Flushed()                         {}

// modelPitch stands in for the pitch element: an input buffer passes the pacer's sink
// probe, is processed at the tempo set by then and leaves as one output buffer.
type modelPitch struct {
	pacer   *audioPacer
	tempo   float64
	tempos  []float64     // every tempo set, in order
	stretch time.Duration // output minus input
}

func newModelPitch(pacer *audioPacer) *modelPitch {
	m := &modelPitch{pacer: pacer, tempo: 1.0}
	pacer.setTempo = func(rate float64) {
		m.tempo = rate
		m.tempos = append(m.tempos, rate)
	}
	return m
}

func newTestPacer() (*modelPitch, *fakeDriftNotifier) {
	tc := &fakeDriftNotifier{}
	return newModelPitch(&audioPacer{tc: tc, tempoAdjustmentRate: 0.05, boostedRate: 0.1}), tc
}

func (m *modelPitch) process(d time.Duration) {
	m.pacer.onInput()
	out := time.Duration(float64(d) / m.tempo)
	m.stretch += out - d
	m.pacer.onOutput(out)
}

// run processes 20ms buffers until no correction is in progress and returns how many it took
func (m *modelPitch) run(max int) int {
	for i := 1; i <= max; i++ {
		m.process(20 * time.Millisecond)
		if !m.pacer.active.Load() {
			return i
		}
	}
	return max
}

// stretchPerBuffer is how much longer than its 20ms of input an output buffer is at a tempo
func stretchPerBuffer(rate float64) time.Duration {
	return time.Duration(float64(20*time.Millisecond) * (1/rate - 1))
}

const creditPrecision = float64(time.Microsecond)

func TestAudioPacerCreditsStretchFromOutput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		drift   time.Duration
		tempo   float64
		buffers int
	}{
		// an output buffer at 0.95 is ≈ 1.053ms longer than its input
		{"audio ahead slows down", 12 * time.Millisecond, 0.95, 12},
		// an output buffer at 1.05 is ≈ 0.952ms shorter than its input
		{"audio behind speeds up", -12 * time.Millisecond, 1.05, 13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, notifier := newTestPacer()

			m.pacer.onOutput(time.Second)
			require.Empty(t, notifier.processed, "output without a correction credits nothing")

			m.pacer.start(tc.drift)
			require.False(t, m.pacer.active.Load(), "a request waits for the next input buffer")
			require.Empty(t, m.tempos)

			m.process(20 * time.Millisecond)
			require.True(t, m.pacer.active.Load())
			require.Equal(t, []float64{tc.tempo}, m.tempos, "the buffer that arms the correction is processed at its tempo")

			// the correction completes on the output buffer that covers the target
			require.Equal(t, tc.buffers-1, m.run(1000))
			require.Equal(t, []float64{tc.tempo, 1.0}, m.tempos)
			require.Len(t, notifier.processed, 1)

			actual := notifier.processed[0]
			perBuffer := stretchPerBuffer(tc.tempo)
			require.InDelta(t, float64(time.Duration(tc.buffers)*perBuffer), float64(actual), creditPrecision)
			require.InDelta(t, float64(m.stretch), float64(actual), creditPrecision, "the pacer reports the stretch pitch produced")
			require.GreaterOrEqual(t, actual.Abs(), tc.drift.Abs())
			require.Less(t, actual.Abs()-tc.drift.Abs(), perBuffer.Abs(), "overshoot is less than one output buffer's stretch")

			for range 10 {
				m.process(20 * time.Millisecond)
			}
			require.Len(t, notifier.processed, 1)
			require.InDelta(t, float64(actual), float64(m.stretch), creditPrecision, "output after completion is produced at tempo 1.0")
		})
	}
}

func TestAudioPacerCreditsOnlyOutput(t *testing.T) {
	m, notifier := newTestPacer()
	m.pacer.start(3 * time.Millisecond)

	// pitch takes five buffers in before it produces anything
	for range 5 {
		m.pacer.onInput()
	}
	require.True(t, m.pacer.active.Load())
	require.Zero(t, m.pacer.compensation.Load(), "input credits nothing")
	require.Empty(t, notifier.processed)

	// one output buffer carries all of it and completes the correction past its target
	in := 100 * time.Millisecond
	out := in + 5*stretchPerBuffer(0.95)
	m.pacer.onOutput(out)
	require.Len(t, notifier.processed, 1)
	require.InDelta(t, float64(out-in), float64(notifier.processed[0]), creditPrecision)
	require.Equal(t, []float64{0.95, 1.0}, m.tempos)
}

func TestAudioPacerAppliesRateRequestOnNextInput(t *testing.T) {
	t.Run("boost and brake during a correction", func(t *testing.T) {
		m, notifier := newTestPacer()
		m.pacer.start(12 * time.Millisecond)
		for range 5 {
			m.process(20 * time.Millisecond)
		}

		m.pacer.boost()
		require.Equal(t, []float64{0.95}, m.tempos, "a boost waits for the next input buffer")
		for range 2 {
			m.process(20 * time.Millisecond)
		}

		m.pacer.brake()
		require.Equal(t, []float64{0.95, 0.9}, m.tempos, "a brake waits for the next input buffer")

		// 5 buffers at 0.95 and 2 at 0.9 produce ≈ 9.71ms, so 3 more at 0.95 cover 12ms
		require.Equal(t, 3, m.run(1000))
		require.Equal(t, []float64{0.95, 0.9, 0.95, 1.0}, m.tempos)
		require.Len(t, notifier.processed, 1)
		require.InDelta(t, float64(8*stretchPerBuffer(0.95)+2*stretchPerBuffer(0.9)), float64(notifier.processed[0]), creditPrecision,
			"each output buffer is credited at the tempo it was produced at")
		require.InDelta(t, float64(m.stretch), float64(notifier.processed[0]), creditPrecision)
	})

	t.Run("boost without a correction", func(t *testing.T) {
		m, _ := newTestPacer()
		m.pacer.boost()
		m.process(20 * time.Millisecond)
		require.Empty(t, m.tempos)
	})

	t.Run("correction requested above the soft budget", func(t *testing.T) {
		m, notifier := newTestPacer()
		notifier.tier = tempo.TierSoft
		m.pacer.start(12 * time.Millisecond)
		m.process(20 * time.Millisecond)
		require.Equal(t, []float64{0.9}, m.tempos)
	})
}

func TestAudioPacerChainedCorrectionsCreditEveryBuffer(t *testing.T) {
	m, notifier := newTestPacer()

	// the controller requests the next correction from DriftProcessed, as the completing buffer leaves pitch
	remaining := 2
	notifier.onProcessed = func() {
		if remaining > 0 {
			remaining--
			m.pacer.start(3 * time.Millisecond)
		}
	}
	m.pacer.start(3 * time.Millisecond)

	buffers := 0
	for len(notifier.processed) < 3 && buffers < 100 {
		m.process(20 * time.Millisecond)
		buffers++
	}

	// each correction takes three buffers, and the next one is armed before the following buffer is processed
	require.Equal(t, 9, buffers)
	require.Equal(t, []float64{0.95, 1.0, 0.95, 1.0, 0.95, 1.0}, m.tempos)
	var total time.Duration
	for _, actual := range notifier.processed {
		require.InDelta(t, float64(3*stretchPerBuffer(0.95)), float64(actual), creditPrecision)
		total += actual
	}
	require.InDelta(t, float64(m.stretch), float64(total), creditPrecision, "no buffer was stretched without credit")
}

func TestAudioPacerFlushCancelsWithoutCredit(t *testing.T) {
	t.Run("correction in progress", func(t *testing.T) {
		m, notifier := newTestPacer()
		m.pacer.start(12 * time.Millisecond)
		m.process(20 * time.Millisecond)

		require.True(t, m.pacer.cancelOnFlush())
		require.False(t, m.pacer.active.Load())
		require.Equal(t, []float64{0.95, 1.0}, m.tempos)

		for range reanchorHold {
			m.process(20 * time.Millisecond)
		}
		require.Empty(t, notifier.processed)

		// the next correction starts from zero
		before := m.stretch
		m.pacer.start(-12 * time.Millisecond)
		require.Equal(t, 13, m.run(1000))
		require.Len(t, notifier.processed, 1)
		require.InDelta(t, float64(m.stretch-before), float64(notifier.processed[0]), creditPrecision)
	})

	t.Run("correction requested", func(t *testing.T) {
		m, notifier := newTestPacer()
		m.pacer.start(12 * time.Millisecond)

		require.True(t, m.pacer.cancelOnFlush(), "the caller hands the request back to the controller")
		for range 10 {
			m.process(20 * time.Millisecond)
		}
		require.Empty(t, m.tempos, "the request is never armed")
		require.Empty(t, notifier.processed)
	})

	t.Run("nothing to cancel", func(t *testing.T) {
		m, _ := newTestPacer()
		require.False(t, m.pacer.cancelOnFlush())
		require.Empty(t, m.tempos)
	})
}

func TestAudioPacerHoldsArmingUntilPitchAnchors(t *testing.T) {
	// pitch anchors its output timeline at a buffer's timestamp divided by the tempo in force;
	// a request that arrives before it has is handed back, and the controller asks again
	requireHeld := func(t *testing.T, m *modelPitch, notifier *fakeDriftNotifier, tempos []float64) {
		for i := range reanchorHold {
			m.pacer.start(12 * time.Millisecond)
			m.process(20 * time.Millisecond)
			require.False(t, m.pacer.active.Load())
			require.Equal(t, i+1, notifier.canceled)
			require.Equal(t, tempos, m.tempos)
		}

		m.pacer.start(12 * time.Millisecond)
		m.process(20 * time.Millisecond)
		require.True(t, m.pacer.active.Load())
		require.Equal(t, reanchorHold, notifier.canceled)
		require.Equal(t, append(tempos, 0.95), m.tempos)
	}

	t.Run("after a flush", func(t *testing.T) {
		m, notifier := newTestPacer()
		m.pacer.start(12 * time.Millisecond)
		m.process(20 * time.Millisecond)
		require.True(t, m.pacer.cancelOnFlush())

		requireHeld(t, m, notifier, []float64{0.95, 1.0})
	})

	t.Run("from the first buffer", func(t *testing.T) {
		notifier := &fakeDriftNotifier{}
		p := newAudioPacer(nil, 0.05, 0.1)
		p.tc = notifier

		requireHeld(t, newModelPitch(p), notifier, nil)
	})
}

func TestAudioPacerTracksDriftOverLongExport(t *testing.T) {
	// a publisher clock 0.2% fast for two hours, sender reports every second, 20ms input buffers
	const skew = 0.002
	tc := tempo.NewController(2750 * time.Millisecond)
	m := newModelPitch(&audioPacer{tc: tc, tempoAdjustmentRate: 0.05, boostedRate: 0.1})
	tc.OnTierChange(func(tier tempo.Tier) {
		if tier == tempo.TierNormal {
			m.pacer.brake()
		} else {
			m.pacer.boost()
		}
	})
	tc.OnDriftDetectedCallback(m.pacer.start)

	var worst time.Duration
	for now := time.Duration(0); now < 2*time.Hour; now += 20 * time.Millisecond {
		if now > 0 && now%time.Second == 0 {
			drift := time.Duration(float64(now) * skew)
			tc.SetDrift(drift)
			worst = max(worst, (drift - m.stretch).Abs())
		}
		m.process(20 * time.Millisecond)
	}

	// drift reaches 14.4s; what pitch produced stays within the threshold plus one buffer's stretch of it
	require.Less(t, worst, tempo.DefaultThreshold+2*time.Millisecond, "worst gap between measured drift and produced stretch")
	require.InDelta(t, float64(m.stretch), float64(tc.Processed()), float64(100*time.Microsecond), "the controller's books match what pitch produced")
}

func TestAudioPacerCorrectsOvershootFromLongBuffer(t *testing.T) {
	// a 1s buffer leaves pitch mid-correction and is credited whole
	tc := tempo.NewController(2750 * time.Millisecond)
	m := newModelPitch(&audioPacer{tc: tc, tempoAdjustmentRate: 0.05, boostedRate: 0.1})
	var targets []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { targets = append(targets, d); m.pacer.start(d) })

	const drift = 12 * time.Millisecond
	tc.SetDrift(drift)
	m.process(20 * time.Millisecond)
	m.process(time.Second)
	for range 100 {
		m.process(20 * time.Millisecond)
	}

	require.Len(t, targets, 2, "the overshoot starts a counter-correction")
	require.Equal(t, drift, targets[0])
	require.Less(t, targets[1], -30*time.Millisecond)
	require.Less(t, (drift - m.stretch).Abs(), tempo.DefaultThreshold, "produced stretch ends within the threshold of the drift")
	require.InDelta(t, float64(m.stretch), float64(tc.Processed()), creditPrecision, "the controller's books match what pitch produced")
}

// pitchHarness runs appsrc → pitch → appsink with the pacer's probes on pitch, and counts
// the audio entering and leaving pitch on its pads. The sink discards what it receives.
type pitchHarness struct {
	t        *testing.T
	pipeline *gst.Pipeline
	src      *app.Source
	sink     *app.Sink
	pitch    *gst.Element
	pacer    *audioPacer
	notifier *fakeDriftNotifier
	seen     atomic.Int64 // pushed buffers that reached pitch's sink pad
	in, out  atomic.Int64 // audio into and out of pitch, in ns
	produced atomic.Int64 // out minus in as the latest output buffer left pitch
	longest  atomic.Int64 // longest output buffer, in ns
	pushed   int
	ahead    bool          // push returns without waiting for pitch
	drained  chan struct{} // closed when a throttled sink has seen EOS
}

const pitchTestCaps = "audio/x-raw,format=F32LE,rate=48000,channels=2,layout=interleaved"

// Pitch processes audio a window at a time. Fed 20ms buffers at tempo 1.0, it holds 18ms to
// 38ms of them right after it produces an output buffer, and produces under 80ms at a time.
const (
	pitchHoldMin   = 10 * time.Millisecond
	pitchHoldMax   = 45 * time.Millisecond
	pitchOutputMax = 80 * time.Millisecond
)

func newPitchHarness(t *testing.T, adjustmentRate float64) *pitchHarness {
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
	sink.SetDrop(true)
	sink.SetMaxBuffers(1)

	pipeline, err := gst.NewPipeline("")
	require.NoError(t, err)
	require.NoError(t, pipeline.AddMany(src.Element, pitch, sink.Element))
	require.NoError(t, gst.ElementLinkMany(src.Element, pitch, sink.Element))

	h := &pitchHarness{t: t, pipeline: pipeline, src: src, sink: sink, pitch: pitch, notifier: &fakeDriftNotifier{}}
	h.pacer = newAudioPacer(pitch, adjustmentRate, 2*adjustmentRate)
	h.pacer.tc = h.notifier
	pitch.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		if buf == nil || buf.Duration() == gst.ClockTimeNone {
			return gst.PadProbeOK
		}
		h.in.Add(int64(*buf.Duration().AsDuration()))
		if buf.GetFlags()&gst.BufferFlagGap == 0 {
			// silence chained in for a GAP event is not one of the pushed buffers
			h.seen.Add(1)
		}
		return gst.PadProbeOK
	})
	installPitchProbes(h.pacer)
	installGapFill(pitch.GetStaticPad("sink"), 48000, 2)
	pitch.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if buf := info.GetBuffer(); buf != nil && buf.Duration() != gst.ClockTimeNone {
			d := int64(*buf.Duration().AsDuration())
			h.produced.Store(h.out.Add(d) - h.in.Load())
			h.longest.Store(max(h.longest.Load(), d))
		}
		return gst.PadProbeOK
	})

	require.NoError(t, pipeline.SetState(gst.StatePlaying))
	t.Cleanup(func() { _ = pipeline.SetState(gst.StateNull) })
	return h
}

// toneBuffer returns the nth 20ms buffer of a 440Hz tone in pitchTestCaps, without a timestamp
func toneBuffer(n int) *gst.Buffer {
	const frames = 960
	data := make([]byte, frames*2*4)
	for f := range frames {
		v := float32(0.3 * math.Sin(2*math.Pi*440*float64(n*frames+f)/48000))
		binary.LittleEndian.PutUint32(data[f*8:], math.Float32bits(v))
		binary.LittleEndian.PutUint32(data[f*8+4:], math.Float32bits(v))
	}
	buf := gst.NewBufferFromBytes(data)
	buf.SetDuration(gst.ClockTime(20 * time.Millisecond))
	return buf
}

// push sends n 20ms buffers of tone and waits until pitch has received them
func (h *pitchHarness) push(n int) {
	for range n {
		buf := toneBuffer(h.pushed)
		buf.SetPresentationTimestamp(gst.ClockTime(time.Duration(h.pushed) * 20 * time.Millisecond))
		require.Equal(h.t, gst.FlowOK, h.src.PushBuffer(buf))
		h.pushed++
	}
	if !h.ahead {
		h.settle()
	}
}

// settle waits until pitch has received every pushed buffer
func (h *pitchHarness) settle() {
	require.Eventually(h.t, func() bool { return h.seen.Load() == int64(h.pushed) }, 2*time.Minute, time.Millisecond)
}

// feedAhead lets push run ahead of pitch by up to backlog of audio, held in the appsrc
func (h *pitchHarness) feedAhead(backlog time.Duration) {
	h.ahead = true
	require.NoError(h.t, h.src.SetProperty("block", true))
	require.NoError(h.t, h.src.SetProperty("max-bytes", uint64(backlog/(20*time.Millisecond))*960*2*4))
}

// throttle makes the sink take one output buffer per interval, so pitch spends its time
// blocked pushing downstream
func (h *pitchHarness) throttle(interval time.Duration) {
	h.sink.SetDrop(false)
	h.drained = make(chan struct{})
	go func() {
		defer close(h.drained)
		for h.sink.PullSample() != nil {
			time.Sleep(interval)
		}
	}()
}

// requireProduced checks, before EOS, that pitch has produced the credited stretch: with it
// accounted for, pitch's output trails its input by the audio pitch holds at tempo 1.0
func (h *pitchHarness) requireProduced(credited time.Duration) {
	h.t.Helper()
	held := credited - time.Duration(h.produced.Load())
	require.GreaterOrEqual(h.t, held, pitchHoldMin, "pitch produced more stretch than was credited")
	require.LessOrEqual(h.t, held, pitchHoldMax, "pitch produced less stretch than was credited")
}

// requireCorrected checks that one correction completed on the output buffer that covered
// the target, and returns what the pacer reported
func (h *pitchHarness) requireCorrected(target time.Duration, rate float64) time.Duration {
	h.t.Helper()
	require.Len(h.t, h.notifier.processed, 1)
	require.False(h.t, h.pacer.active.Load())
	require.InDelta(h.t, 1.0, h.tempo(), 1e-6)
	actual := h.notifier.processed[0]
	require.GreaterOrEqual(h.t, actual.Abs(), target.Abs())
	require.Less(h.t, time.Duration(h.longest.Load()), pitchOutputMax)
	require.Less(h.t, actual.Abs()-target.Abs(), time.Duration(float64(pitchOutputMax)*rate), "overshoot is less than one output buffer's stretch")
	return actual
}

// tempo is the tempo set on the pitch element
func (h *pitchHarness) tempo() float64 {
	v, err := h.pitch.GetProperty("tempo")
	require.NoError(h.t, err)
	return float64(v.(float32))
}

// finish ends the stream and returns output minus pushed input. Pitch completes its output
// at EOS from the tempo and the input it was given, so this measures only a stream that
// stayed at tempo 1.0.
func (h *pitchHarness) finish() time.Duration {
	h.src.EndStream()
	if h.drained != nil {
		<-h.drained
	}
	sample := h.sink.PullSample()
	for sample != nil {
		sample = h.sink.PullSample()
	}
	return time.Duration(h.out.Load()) - time.Duration(h.pushed)*20*time.Millisecond
}

func TestAudioPacerPitchProducesCreditedStretch(t *testing.T) {
	t.Run("no correction", func(t *testing.T) {
		h := newPitchHarness(t, 0.05)
		h.push(150)
		h.requireProduced(0)
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
		{"long slow down", 0.05, 200 * time.Millisecond},
		{"long speed up", 0.05, -200 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// requested mid-stream from another thread, as a sender report does, with pitch holding audio
			h := newPitchHarness(t, tc.rate)
			h.push(53)
			h.pacer.start(tc.drift)
			h.push(400)

			h.requireProduced(h.requireCorrected(tc.drift, tc.rate))
		})
	}
}

func TestAudioPacerPitchFlushCancelsCorrection(t *testing.T) {
	flush := func(t *testing.T, h *pitchHarness) {
		srcPad := h.src.GetStaticPad("src")
		require.True(t, srcPad.PushEvent(gst.NewFlushStartEvent()))
		require.True(t, srcPad.PushEvent(gst.NewFlushStopEvent(false)))
	}

	t.Run("correction in progress", func(t *testing.T) {
		h := newPitchHarness(t, 0.05)
		h.push(20)
		h.pacer.start(200 * time.Millisecond)
		h.push(10)
		require.True(t, h.pacer.active.Load())
		require.InDelta(t, 0.95, h.tempo(), 1e-6)

		flush(t, h)
		require.False(t, h.pacer.active.Load(), "a flush reaching pitch cancels the correction")
		require.InDelta(t, 1.0, h.tempo(), 1e-6)
		require.Equal(t, 1, h.notifier.canceled)
		require.Empty(t, h.notifier.processed)
	})

	t.Run("correction requested", func(t *testing.T) {
		// no input buffer follows the request, so the flush finds it waiting
		h := newPitchHarness(t, 0.05)
		h.push(20)
		h.pacer.start(200 * time.Millisecond)

		flush(t, h)
		require.Zero(t, h.pacer.pending.Load(), "a flush reaching pitch drops the request")
		require.Equal(t, 1, h.notifier.canceled, "the request is handed back to the controller")
		require.InDelta(t, 1.0, h.tempo(), 1e-6)
	})
}

func TestAudioPacerGapEventBecomesSilenceBeforePitch(t *testing.T) {
	h := newPitchHarness(t, 0.05)
	h.push(50)

	// a 1.3s mute signaled as a GAP event, as the replay source does, between two runs of audio
	srcPad := h.src.GetStaticPad("src")
	require.True(t, srcPad.PushEvent(gst.NewGapEvent(gst.ClockTime(time.Second), gst.ClockTime(1300*time.Millisecond))))
	h.push(50)

	require.InDelta(t, float64(1300*time.Millisecond), float64(h.finish()), float64(100*time.Microsecond),
		"pitch emits the gap as audio, so later audio keeps its place")
}

func TestAudioPacerGapBeforeAudioPassesThrough(t *testing.T) {
	// a replay track that starts muted sends its GAP before any audio; pitch has no caps yet,
	// so the GAP must reach the mixer as an event rather than vanish
	h := newPitchHarness(t, 0.05)
	var forwarded atomic.Int64
	h.pitch.GetStaticPad("src").AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if ev := info.GetEvent(); ev != nil && ev.Type() == gst.EventTypeGap {
			forwarded.Add(1)
		}
		return gst.PadProbeOK
	})

	srcPad := h.src.GetStaticPad("src")
	require.True(t, srcPad.PushEvent(gst.NewGapEvent(0, gst.ClockTime(time.Second))))
	h.push(50)

	require.Eventually(t, func() bool { return forwarded.Load() == 1 }, time.Second, time.Millisecond, "the GAP is forwarded, not dropped")
	require.InDelta(t, 0, float64(h.finish()), float64(100*time.Microsecond), "no silence was chained for it")
}

func TestAudioPacerGapBeforeSegmentBecomesSilence(t *testing.T) {
	// a replay track that starts muted: its first GAP reaches pitch with caps opusdec negotiated
	// for it but no segment, and the depayloader's segment arrives with the first packet, equal
	// to a default one except for its position
	initGStreamer(t)
	const frames = 50
	packets := opusRTPPackets(t, frames)
	run := func(gap time.Duration) (out time.Duration, segments int32, segmentBeforeData bool) {
		p, err := gst.NewPipelineFromString("appsrc name=src format=time caps=application/x-rtp,media=audio,encoding-name=OPUS,clock-rate=48000,payload=111 ! " +
			"rtpopusdepay ! opusdec ! audioconvert ! audioresample ! " + pitchTestCaps + " ! pitch name=pitch ! appsink name=out sync=false")
		if err != nil {
			t.Skip("rtpopusdepay, opusdec or pitch not available")
		}
		srcEl, err := p.GetElementByName("src")
		require.NoError(t, err)
		pitchEl, err := p.GetElementByName("pitch")
		require.NoError(t, err)
		outEl, err := p.GetElementByName("out")
		require.NoError(t, err)
		src, sink := app.SrcFromElement(srcEl), app.SinkFromElement(outEl)
		installGapFill(pitchEl.GetStaticPad("sink"), 48000, 2)

		var segmentCount, firstOutput atomic.Int32
		pitchSrc := pitchEl.GetStaticPad("src")
		pitchSrc.AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			if ev := info.GetEvent(); ev != nil && ev.Type() == gst.EventTypeSegment {
				segmentCount.Add(1)
			}
			return gst.PadProbeOK
		})
		pitchSrc.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
			if firstOutput.Load() == 0 {
				if pad.GetStickyEvent(gst.EventTypeSegment, 0) != nil {
					firstOutput.Store(1)
				} else {
					firstOutput.Store(-1)
				}
			}
			return gst.PadProbeOK
		})

		require.NoError(t, p.SetState(gst.StatePlaying))
		defer func() { _ = p.SetState(gst.StateNull) }()

		if gap > 0 {
			require.True(t, src.SendEvent(gst.NewGapEvent(0, gst.ClockTime(gap))))
		}
		for i, pkt := range packets {
			data, err := pkt.Marshal()
			require.NoError(t, err)
			buf := gst.NewBufferFromBytes(data)
			buf.SetPresentationTimestamp(gst.ClockTime(gap + time.Duration(i)*20*time.Millisecond))
			require.Equal(t, gst.FlowOK, src.PushBuffer(buf))
		}
		src.EndStream()

		for {
			sample := sink.PullSample()
			if sample == nil {
				break
			}
			if d := sample.GetBuffer().Duration(); d != gst.ClockTimeNone {
				out += *d.AsDuration()
			}
		}
		return out, segmentCount.Load(), firstOutput.Load() == 1
	}

	baseline, _, _ := run(0)
	require.Greater(t, baseline, time.Duration(0))

	out, segments, segmentBeforeData := run(time.Second)
	require.InDelta(t, float64(baseline+time.Second), float64(out), float64(100*time.Microsecond),
		"the GAP became a second of silence and none of it was discarded")
	require.True(t, segmentBeforeData, "the silence left pitch inside a segment")
	require.Equal(t, int32(1), segments, "the depayloader's segment, differing only in position, never reached pitch")
}

// opusRTPPackets returns n RTP packets of 20ms mono Opus frames, 960 samples apart
func opusRTPPackets(t *testing.T, n int) []*rtp.Packet {
	t.Helper()
	// the encoder pads the stream with one more frame at EOS
	p, err := gst.NewPipelineFromString("audiotestsrc num-buffers=" + strconv.Itoa(n) + " samplesperbuffer=960 wave=sine ! audioconvert ! audioresample ! " +
		"audio/x-raw,rate=48000,channels=1 ! opusenc frame-size=20 ! appsink name=enc sync=false")
	if err != nil {
		t.Skip("opusenc not available")
	}
	encEl, err := p.GetElementByName("enc")
	require.NoError(t, err)
	enc := app.SinkFromElement(encEl)
	require.NoError(t, p.SetState(gst.StatePlaying))
	defer func() { _ = p.SetState(gst.StateNull) }()

	var packets []*rtp.Packet
	for len(packets) < n {
		sample := enc.PullSample()
		require.NotNil(t, sample, "opusenc produced fewer frames than requested")
		i := len(packets)
		packets = append(packets, &rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 111, SSRC: 1, SequenceNumber: uint16(i), Timestamp: uint32(i * 960)},
			Payload: append([]byte(nil), sample.GetBuffer().Bytes()...),
		})
	}
	return packets
}

func TestAudioPacerCorrectionCompletesOnGapSilence(t *testing.T) {
	// a 12ms correction is armed on the 51st buffer, right before a 1.3s mute
	h := newPitchHarness(t, 0.05)
	h.push(50)
	h.pacer.start(12 * time.Millisecond)
	h.push(1)

	srcPad := h.src.GetStaticPad("src")
	require.True(t, srcPad.PushEvent(gst.NewGapEvent(gst.ClockTime(1020*time.Millisecond), gst.ClockTime(1300*time.Millisecond))))
	h.push(50)

	// the silence is stretched like speech and reaches pitch in packet-sized buffers, so it
	// leaves pitch in buffers no longer than speech does and the mute is not credited whole
	h.requireProduced(h.requireCorrected(12*time.Millisecond, 0.05))
}

// A publisher clock 0.3% off for 20 minutes with a sender report every second, the tempo
// controller driving a real pitch element. Hundreds of corrections end with audio still
// inside pitch, and what pitch has produced before EOS is what the controller credited.
func TestAudioPacerPitchTracksDriftOverLongStream(t *testing.T) {
	for _, tc := range []struct {
		name string
		skew float64
		// the feeder runs 14s ahead of pitch and the sink takes its time, as when a
		// recording is processed faster than real time
		ahead bool
	}{
		{"slow clock", 0.003, false},
		{"fast clock", -0.003, false},
		{"slow clock, feeder ahead of pitch, slow sink", 0.003, true},
		{"fast clock, feeder ahead of pitch, slow sink", -0.003, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := tempo.NewController(2750 * time.Millisecond)
			h := newPitchHarness(t, 0.05)
			h.pacer.tc = ctrl
			ctrl.OnDriftDetectedCallback(h.pacer.start)
			if tc.ahead {
				h.feedAhead(14 * time.Second)
				h.throttle(200 * time.Microsecond)
			}

			const seconds = 20 * 60
			var drift time.Duration
			for s := 1; s <= seconds; s++ {
				h.push(50)
				drift = time.Duration(float64(time.Duration(s)*time.Second) * tc.skew)
				ctrl.SetDrift(drift)
			}
			h.push(100)
			h.settle()

			require.False(t, h.pacer.active.Load())
			credited := ctrl.Processed()
			require.InDelta(t, float64(drift), float64(credited), float64(15*time.Millisecond), "the controller credits the drift")
			h.requireProduced(credited)
		})
	}
}
