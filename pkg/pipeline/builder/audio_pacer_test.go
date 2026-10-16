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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/pipeline/tempo"
)

type fakeDriftNotifier struct {
	processed []time.Duration
	canceled  int
	tier      tempo.Tier
}

func (f *fakeDriftNotifier) DriftProcessed(actual time.Duration) {
	f.processed = append(f.processed, actual)
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

// feed pushes 20ms input buffers until the correction completes and returns how many it took
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
		name    string
		drift   time.Duration
		tempo   float64
		buffers int
	}{
		// 20ms × (1/0.95 − 1) ≈ 1.053ms per buffer
		{"audio ahead slows down", 12 * time.Millisecond, 0.95, 12},
		// 20ms × (1/1.05 − 1) ≈ −0.952ms per buffer
		{"audio behind speeds up", -12 * time.Millisecond, 1.05, 13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, notifier, tempos := newTestPacer()
			p.start(tc.drift)

			require.Equal(t, tc.buffers, feed(p, 1000))
			require.Equal(t, []float64{tc.tempo, 1.0}, *tempos)
			require.Len(t, notifier.processed, 1)

			actual := notifier.processed[0]
			require.Equal(t, tc.drift > 0, actual > 0, "the credited stretch has the target's sign")
			require.GreaterOrEqual(t, actual.Abs(), tc.drift.Abs())
			require.Less(t, actual.Abs()-tc.drift.Abs(), 1100*time.Microsecond, "overshoot is at most one input buffer's stretch")

			p.onInput(20 * time.Millisecond)
			require.Len(t, notifier.processed, 1, "input after completion credits nothing")
		})
	}
}

func TestAudioPacerCreditsBoostedTempo(t *testing.T) {
	p, notifier, tempos := newTestPacer()
	p.start(12 * time.Millisecond)

	// 5 buffers at 0.95 credit ≈ 5.26ms, then 0.9 credits ≈ 2.22ms per buffer
	for range 5 {
		p.onInput(20 * time.Millisecond)
	}
	p.boost()
	require.Equal(t, 4, feed(p, 1000))

	require.Equal(t, []float64{0.95, 0.9, 1.0}, *tempos)
	require.Len(t, notifier.processed, 1)
	require.InDelta(t, float64(5*20*time.Millisecond/19+4*20*time.Millisecond/9), float64(notifier.processed[0]), float64(time.Microsecond))
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
	require.Equal(t, 13, feed(p, 1000))
	require.Len(t, notifier.processed, 1)
	require.Less(t, notifier.processed[0], time.Duration(0), "a new correction starts from zero")
}
