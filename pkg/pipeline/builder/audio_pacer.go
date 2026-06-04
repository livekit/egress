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
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/linkdata/deadlock"

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
	tc                  driftProcessNotifier
	tempoAdjustmentRate float64
	boostedRate         float64

	// mu serializes writers; probe reads active/snapshot without it.
	mu       deadlock.Mutex
	active   atomic.Bool
	snapshot atomic.Pointer[pacerSnapshot]

	inputAccum  atomic.Int64 // cumulative input buffer durations (nanoseconds)
	outputAccum atomic.Int64 // cumulative output buffer durations (nanoseconds)
}

type pacerSnapshot struct {
	inputAtStart  int64
	outputAtStart int64
	targetDrift   time.Duration
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

	// OnTierChange fires only on transitions; check Tier() to start boosted on a stable elevated state.
	adjustment := a.tempoAdjustmentRate
	if a.tc != nil && a.tc.Tier() >= tempo.TierSoft {
		adjustment = a.boostedRate
	}
	rate := pacerRate(drift, adjustment)

	// snapshot before active: probe reads active first.
	a.snapshot.Store(&pacerSnapshot{
		inputAtStart:  a.inputAccum.Load(),
		outputAtStart: a.outputAccum.Load(),
		targetDrift:   drift,
	})

	logger.Debugw("starting audio pacer", "targetDrift", drift, "rate", rate)
	a.pitch.SetArg("tempo", fmt.Sprintf("%.2f", rate))
	a.active.Store(true)
}

func (a *audioPacer) boost() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.active.Load() {
		return
	}

	snap := a.snapshot.Load()
	if snap == nil {
		return
	}
	rate := pacerRate(snap.targetDrift, a.boostedRate)
	a.pitch.SetArg("tempo", fmt.Sprintf("%.2f", rate))
}

func (a *audioPacer) brake() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.active.Load() {
		return
	}

	snap := a.snapshot.Load()
	if snap == nil {
		return
	}
	rate := pacerRate(snap.targetDrift, a.tempoAdjustmentRate)
	a.pitch.SetArg("tempo", fmt.Sprintf("%.2f", rate))
}

// Returns true on the active→idle transition; caller then notifies the controller.
func (a *audioPacer) stop() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.active.Swap(false) {
		return false
	}
	a.pitch.SetArg("tempo", fmt.Sprintf("%.1f", 1.0))
	return true
}

// Accumulators can't be trusted across the flush boundary; abandon without crediting compensation.
func (a *audioPacer) cancelOnFlush() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.active.Swap(false) {
		return false
	}
	a.pitch.SetArg("tempo", fmt.Sprintf("%.1f", 1.0))
	return true
}

// drift>0 → audio ahead → tempo<1 (slow); drift<0 → tempo>1 (speed up).
func pacerRate(drift time.Duration, adjustment float64) float64 {
	if drift > 0 {
		return 1 - adjustment
	}
	return 1 + adjustment
}
