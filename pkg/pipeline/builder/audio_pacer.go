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
	"time"

	"github.com/go-gst/go-gst/gst"
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
type audioPacer struct {
	pitch               *gst.Element
	active              atomic.Bool
	tc                  driftProcessNotifier
	tempoAdjustmentRate float64
	boostedRate         float64

	// Continuous accumulators for measuring actual compensation.
	// Compensation = outputAccum - inputAccum (relative to snapshots at start).
	inputAccum  atomic.Int64 // cumulative input buffer durations (nanoseconds)
	outputAccum atomic.Int64 // cumulative output buffer durations (nanoseconds)

	// snapshot is the active correction's baseline. nil when idle.
	snapshot atomic.Pointer[pacerSnapshot]
}

// pacerSnapshot is the per-correction baseline captured at start(). It is
// stored atomically as a unit so the probe never observes a torn snapshot
// even if start() is mis-invoked while a correction is still active. The
// active check in start() prevents this today, but treating the snapshot as
// an atomic value removes the implicit ordering dependency between
// active.Store and the snapshot field writes.
type pacerSnapshot struct {
	inputAtStart  int64
	outputAtStart int64
	targetDrift   time.Duration
}

func (a *audioPacer) start(drift time.Duration) {
	if a.pitch == nil || drift == 0 {
		return
	}
	if a.active.Load() {
		logger.Errorw(
			"starting audio pacer, but it's already active",
			errors.New("tempo controller bug"),
		)
		return
	}

	// If we're already past the soft budget, start boosted so we don't burn a
	// full correction cycle at the base rate before the tier-change callback
	// catches up.
	adjustment := a.tempoAdjustmentRate
	if a.tc != nil && a.tc.Tier() >= tempo.TierSoft {
		adjustment = a.boostedRate
	}
	rate := pacerRate(drift, adjustment)

	// Publish the snapshot before active=true so the probe — which reads
	// active first, then snapshot — never sees active without a snapshot.
	a.snapshot.Store(&pacerSnapshot{
		inputAtStart:  a.inputAccum.Load(),
		outputAtStart: a.outputAccum.Load(),
		targetDrift:   drift,
	})

	logger.Debugw("starting audio pacer", "targetDrift", drift, "rate", rate)
	a.pitch.SetArg("tempo", fmt.Sprintf("%.2f", rate))
	a.active.Store(true)
}

// boost switches the pitch element to a more aggressive rate
func (a *audioPacer) boost() {
	if a.pitch == nil || !a.active.Load() {
		return
	}
	snap := a.snapshot.Load()
	if snap == nil {
		return
	}
	rate := pacerRate(snap.targetDrift, a.boostedRate)
	a.pitch.SetArg("tempo", fmt.Sprintf("%.2f", rate))
}

// brake restores the base rate when drift falls back inside the soft budget
func (a *audioPacer) brake() {
	if a.pitch == nil || !a.active.Load() {
		return
	}
	snap := a.snapshot.Load()
	if snap == nil {
		return
	}
	rate := pacerRate(snap.targetDrift, a.tempoAdjustmentRate)
	a.pitch.SetArg("tempo", fmt.Sprintf("%.2f", rate))
}

// stop is the success path used by the src-pad probe when the in-flight
// correction has reached its target. Returns true if this call won the
// transition from active to idle; the caller must then notify the controller
// via DriftProcessed. Concurrent stop / cancelOnFlush calls coalesce: only
// one wins the Swap, the others see false and do nothing.
func (a *audioPacer) stop() bool {
	if a.pitch == nil {
		return false
	}
	if !a.active.Swap(false) {
		return false
	}
	a.pitch.SetArg("tempo", fmt.Sprintf("%.1f", 1.0))
	return true
}

// cancelOnFlush is the abort path used when downstream is about to discard
// the audio the in-flight correction has been applying to. The measurement
// in inputAccum / outputAccum cannot be trusted across the flush boundary,
// so the correction is abandoned: tempo returns to 1.0, the controller's
// in-flight target is cleared (without crediting any compensation), and the
// next SR drives a fresh correction. Returns true if this call won the
// transition.
func (a *audioPacer) cancelOnFlush() bool {
	if a.pitch == nil {
		return false
	}
	if !a.active.Swap(false) {
		return false
	}
	a.pitch.SetArg("tempo", fmt.Sprintf("%.1f", 1.0))
	return true
}

// pacerRate computes the tempo property value for the given drift direction
// at the given per-step adjustment rate. drift>0 means audio is ahead of wall
// clock, so tempo<1 (slow down); drift<0 means audio is behind, so tempo>1.
func pacerRate(drift time.Duration, adjustment float64) float64 {
	if drift > 0 {
		return 1 - adjustment
	}
	return 1 + adjustment
}
