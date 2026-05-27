package tempo

import (
	"time"

	"github.com/linkdata/deadlock"
)

const (
	// DefaultThreshold is the minimum effective drift before a correction starts.
	DefaultThreshold = 10 * time.Millisecond
)

// Controller tracks the current drift between a sender's clock and the
// receiver's clock and drives tempo adjustments to close the gap.
//
// It works as a "running target" model: each SetDrift call updates the
// current drift, and the controller corrects when the uncorrected
// (effective) drift exceeds the threshold.
type Controller struct {
	mu deadlock.Mutex

	drift     time.Duration // latest raw drift from measurement
	corrected time.Duration // sum of all corrections applied by the pacer
	current   time.Duration // correction currently in progress (0 if idle)

	cb func(time.Duration) // invoked with correction amount to apply
}

func NewController() *Controller { return &Controller{} }

// SetDrift updates the current observed drift. If no correction is active
// and the effective (uncorrected) drift exceeds the threshold, a new
// correction is started.
func (tc *Controller) SetDrift(drift time.Duration) {
	tc.mu.Lock()
	tc.drift = drift

	var toStart time.Duration
	if tc.current == 0 {
		effective := drift - tc.corrected
		if effective.Abs() >= DefaultThreshold {
			toStart = effective
			tc.current = effective
		}
	}
	cb := tc.cb
	tc.mu.Unlock()

	if toStart != 0 && cb != nil {
		cb(toStart)
	}
}

// DriftProcessed marks the current correction as finished with the given
// actual compensation and may start the next correction if effective drift
// still exceeds the threshold.
func (tc *Controller) DriftProcessed(actual time.Duration) {
	tc.mu.Lock()
	tc.corrected += actual
	tc.current = 0

	// Clamp corrected if it overshot drift in the same direction as the just-
	// applied correction. Without this, a per-buffer overshoot can flip the
	// residual sign and immediately queue a phantom counter-correction.
	if (actual > 0 && tc.corrected > tc.drift) || (actual < 0 && tc.corrected < tc.drift) {
		tc.corrected = tc.drift
	}

	var toStart time.Duration
	effective := tc.drift - tc.corrected
	if effective.Abs() >= DefaultThreshold {
		toStart = effective
		tc.current = effective
	}
	cb := tc.cb
	tc.mu.Unlock()

	if toStart != 0 && cb != nil {
		cb(toStart)
	}
}

// OnDriftDetectedCallback sets the callback. If a correction is already armed,
// it's invoked immediately with that value.
func (tc *Controller) OnDriftDetectedCallback(cb func(time.Duration)) {
	tc.mu.Lock()
	tc.cb = cb
	cur := tc.current
	tc.mu.Unlock()

	if cb != nil && cur != 0 {
		cb(cur)
	}
}

// CancelInFlight clears the in-flight correction without crediting any
// compensation toward `corrected`. Use when the audio pipeline downstream of
// the pacer is about to be discarded (source bin reset, flush) and the
// partial compensation already pushed downstream is being thrown away.
//
// Distinct from DriftProcessed(0): that path would race with the same
// semantics here, but conceptually means "the pacer finished and applied
// nothing." This path means "abandon the in-flight target; the next SR will
// re-measure drift from scratch." A subsequent SetDrift / OnDriftDetectedCallback
// will not re-fire the old target value (which would arm the new pacer with
// the lost work) because current is cleared here.
//
// corrected is intentionally left untouched: prior completed corrections
// remain on the books, since their audio is downstream of the dropped buffer
// range.
func (tc *Controller) CancelInFlight() {
	tc.mu.Lock()
	tc.current = 0
	tc.mu.Unlock()
}

// Processed returns the total of already-applied corrections.
func (tc *Controller) Processed() time.Duration {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.corrected
}
