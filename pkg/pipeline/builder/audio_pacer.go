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
	ObserveOutputShift(shift time.Duration)
	Flushed()
}

// audioPacer corrects drift by changing the tempo of the pitch element until the
// stretch pitch has produced covers the target. Pitch applies a tempo to the audio it
// processes, a window at a time, so an output buffer produced at tempo t is (1 - t) of
// its duration longer than the audio it consumed; the stretch is accumulated from output
// buffers. A correction starts, changes rate and completes on pitch's streaming thread,
// between buffers, so every output buffer is produced at one tempo.
type audioPacer struct {
	pitch               *gst.Element
	setTempo            func(rate float64)
	tc                  driftProcessNotifier
	tempoAdjustmentRate float64
	boostedRate         float64

	// mu serializes the streaming thread with a flush cancel; the output probe reads active/target/rate without it.
	mu           deadlock.Mutex
	active       atomic.Bool
	target       atomic.Duration
	rate         atomic.Float64 // tempo currently applied
	compensation atomic.Duration

	// requests, taken on the streaming thread as the next input buffer arrives
	pending atomic.Duration
	adjust  atomic.Int32

	// input buffers still to pass before a correction can be armed
	reanchorBuffers atomic.Int32

	lastInEnd atomic.Duration // 0 after a flush
}

const (
	adjustNone int32 = iota
	adjustBoost
	adjustBrake
)

// reanchorHold is the number of input buffers that pass at tempo 1.0 once the pacer is
// created or pitch is flushed. Pitch anchors its output timeline on the first buffer it
// processes, at the buffer's timestamp divided by the tempo; a buffer that reached the
// sink pad as the flush started is processed before pitch resets, and takes one.
const reanchorHold = 2

func newAudioPacer(pitch *gst.Element, adjustmentRate, boostedRate float64) *audioPacer {
	a := &audioPacer{
		pitch: pitch,
		setTempo: func(rate float64) {
			pitch.SetArg("tempo", fmt.Sprintf("%.2f", rate))
		},
		tempoAdjustmentRate: adjustmentRate,
		boostedRate:         boostedRate,
	}
	a.reanchorBuffers.Store(reanchorHold)
	return a
}

// start requests a correction, which is armed as the next input buffer reaches pitch.
func (a *audioPacer) start(drift time.Duration) {
	if drift == 0 {
		return
	}
	a.pending.Store(drift)
}

func (a *audioPacer) arm(drift time.Duration) {
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
		// the next sender report requests the correction again
		logger.Debugw("audio pacer not armed until pitch anchors its output", "targetDrift", drift)
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

	a.target.Store(drift)
	a.compensation.Store(0)
	a.applyTempoLocked(rate)

	logger.Debugw("starting audio pacer", "targetDrift", drift, "rate", rate)
	a.active.Store(true)
}

// boost and brake request a rate for the correction in progress, applied as the next
// input buffer reaches pitch.
func (a *audioPacer) boost() {
	a.adjust.Store(adjustBoost)
}

func (a *audioPacer) brake() {
	a.adjust.Store(adjustBrake)
}

func (a *audioPacer) applyAdjust(adjust int32) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.active.Load() {
		return
	}
	adjustment := a.tempoAdjustmentRate
	if adjust == adjustBoost {
		adjustment = a.boostedRate
	}
	a.applyTempoLocked(pacerRate(a.target.Load(), adjustment))
}

// applyTempoLocked sets the tempo and records it as pitch applies it, to two decimals.
func (a *audioPacer) applyTempoLocked(rate float64) {
	rate = math.Round(rate*100) / 100
	a.rate.Store(rate)
	a.setTempo(rate)
}

// onInput runs on pitch's sink pad before pitch processes the buffer, so the buffer and
// the audio pitch holds are processed at the tempo set when it returns.
func (a *audioPacer) onInput() {
	if drift := a.pending.Swap(0); drift != 0 {
		a.arm(drift)
	}
	if adjust := a.adjust.Swap(adjustNone); adjust != adjustNone {
		a.applyAdjust(adjust)
	}
	if n := a.reanchorBuffers.Load(); n > 0 {
		// a flush that restarts the hold meanwhile keeps its count
		a.reanchorBuffers.CompareAndSwap(n, n-1)
	}
}

// onOutput runs on pitch's src pad for each buffer pitch produces and credits the stretch
// in it. The correction completes on the buffer that covers the target, so it can exceed the
// target by one buffer's stretch; the controller is told what was produced.
func (a *audioPacer) onOutput(d time.Duration) {
	if !a.active.Load() {
		return
	}
	produced := a.compensation.Add(time.Duration(float64(d) * (1 - a.rate.Load())))
	if produced.Abs() >= a.target.Load().Abs() {
		a.complete()
	}
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

// Pitch drops its buffered audio on flush and anchors its output again on the next buffer;
// a requested or active correction is abandoned without credit. Returns whether there was one.
func (a *audioPacer) cancelOnFlush() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.reanchorBuffers.Store(reanchorHold)
	a.lastInEnd.Store(0)
	a.adjust.Store(adjustNone)
	requested := a.pending.Swap(0) != 0
	if !a.active.Swap(false) {
		return requested
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
