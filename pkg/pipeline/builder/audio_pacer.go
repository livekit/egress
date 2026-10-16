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
	"math"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/linkdata/deadlock"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/tempo"
	"github.com/livekit/protocol/logger"
)

type driftProcessNotifier interface {
	DriftProcessed(actual time.Duration)
	CancelInFlight()
	Tier() tempo.Tier
}

// audioPacer corrects drift by changing the tempo of the pitch element until the
// stretch it has applied covers the target. Pitch stretches each input buffer by
// (1/tempo - 1) of its duration, so the stretch is accumulated from input buffers:
// pitch emits output in chunks of several input buffers, so durations measured at
// its output swing by tens of milliseconds regardless of tempo.
type audioPacer struct {
	pitch               *gst.Element
	setTempo            func(rate float64)
	tc                  driftProcessNotifier
	tempoAdjustmentRate float64
	boostedRate         float64

	// mu serializes writers; the input probe reads active/target/rate without it.
	mu           deadlock.Mutex
	active       atomic.Bool
	target       atomic.Duration
	rate         atomic.Float64 // tempo currently applied
	compensation atomic.Duration

	// buffers that pass at tempo 1.0 after a flush, until pitch has re-anchored
	reanchorBuffers atomic.Int32
}

func newAudioPacer(pitch *gst.Element, adjustmentRate, boostedRate float64) *audioPacer {
	return &audioPacer{
		pitch: pitch,
		setTempo: func(rate float64) {
			pitch.SetArg("tempo", fmt.Sprintf("%.2f", rate))
		},
		tempoAdjustmentRate: adjustmentRate,
		boostedRate:         boostedRate,
	}
}

func (a *audioPacer) start(drift time.Duration) {
	if drift == 0 {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.active.Load() {
		logger.Errorw(
			"starting audio pacer, but it's already active",
			errors.New("tempo controller bug"),
		)
		return
	}
	if a.reanchorBuffers.Load() > 0 {
		// pitch re-anchors at timestamp / tempo; the next sender report arms again
		logger.Debugw("audio pacer not armed until pitch re-anchors after flush", "targetDrift", drift)
		if a.tc != nil {
			a.tc.CancelInFlight()
		}
		return
	}

	// OnTierChange fires only on transitions; check Tier() to start boosted on a stable elevated state.
	adjustment := a.tempoAdjustmentRate
	if a.tc != nil && a.tc.Tier() >= tempo.TierSoft {
		adjustment = a.boostedRate
	}
	rate := pacerRate(drift, adjustment)

	// state before active: the input probe reads active first.
	a.target.Store(drift)
	a.compensation.Store(0)
	a.applyTempoLocked(rate)

	logger.Debugw("starting audio pacer", "targetDrift", drift, "rate", rate)
	a.active.Store(true)
}

func (a *audioPacer) boost() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.active.Load() {
		return
	}
	a.applyTempoLocked(pacerRate(a.target.Load(), a.boostedRate))
}

func (a *audioPacer) brake() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.active.Load() {
		return
	}
	a.applyTempoLocked(pacerRate(a.target.Load(), a.tempoAdjustmentRate))
}

// applyTempoLocked sets the tempo and records it as pitch applies it, to two decimals.
func (a *audioPacer) applyTempoLocked(rate float64) {
	rate = math.Round(rate*100) / 100
	a.rate.Store(rate)
	a.setTempo(rate)
}

// onInput runs on pitch's sink pad before pitch processes the buffer, so the buffer is
// stretched at whatever tempo is set when onInput returns. Buffers already credited were
// processed at the pacer's tempo; once they cover the target, the correction completes
// before this buffer is processed, and this buffer counts toward a correction the
// controller starts from DriftProcessed.
func (a *audioPacer) onInput(d time.Duration) {
	if n := a.reanchorBuffers.Load(); n > 0 {
		// the buffer re-anchors pitch after this probe returns
		a.reanchorBuffers.Store(n - 1)
		return
	}
	if !a.active.Load() {
		return
	}
	if a.compensation.Load().Abs() >= a.target.Load().Abs() {
		a.complete()
		if !a.active.Load() {
			return
		}
	}

	rate := a.rate.Load()
	if rate <= 0 {
		return
	}
	a.compensation.Add(time.Duration(float64(d) * (1/rate - 1)))
}

func (a *audioPacer) complete() {
	target, compensation := a.target.Load(), a.compensation.Load()
	// stop() returns false if a concurrent flush cancel already won the transition;
	// in that case the controller has already been notified.
	if a.stop() {
		logger.Debugw("audio drift corrected", "target", target, "actual", compensation)
		if a.tc != nil {
			a.tc.DriftProcessed(compensation)
		}
	}
}

// Returns true on the active→idle transition; caller then notifies the controller.
func (a *audioPacer) stop() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.active.Swap(false) {
		return false
	}
	a.applyTempoLocked(1.0)
	return true
}

// Pitch drops its buffered audio on flush, so part of the credited stretch never
// reaches the mixer; abandon the correction without crediting it.
func (a *audioPacer) cancelOnFlush() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.reanchorBuffers.Store(2)
	if !a.active.Swap(false) {
		return false
	}
	a.applyTempoLocked(1.0)
	return true
}

// drift = sender-report time minus RTP-derived time (the legacy synchronizer's sign):
// drift>0 → publisher clock slow → tempo<1 adds samples; drift<0 → tempo>1 removes them.
func pacerRate(drift time.Duration, adjustment float64) float64 {
	if drift > 0 {
		return 1 - adjustment
	}
	return 1 + adjustment
}
