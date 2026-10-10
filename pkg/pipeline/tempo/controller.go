package tempo

import (
	"time"

	"github.com/linkdata/deadlock"
)

const (
	// DefaultThreshold is the minimum effective drift before a correction starts.
	DefaultThreshold = 10 * time.Millisecond
)

// Tier indicates how far effective drift has grown relative to the configured
// budgets. The pacer / caller wires escalation behavior to tier transitions.
type Tier int

const (
	TierNormal Tier = iota // |effective drift| < soft budget
	TierSoft               // soft budget exceeded; pacer should boost
	TierHard               // hard budget exceeded; caller should reset
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

	softCap    time.Duration
	hardCap    time.Duration
	maxLatency time.Duration
	tier       Tier

	cb     func(time.Duration) // invoked with correction amount to apply
	tierCB func(Tier)          // invoked on tier transitions
}

func NewController(maxLatency time.Duration) *Controller {
	return &Controller{
		softCap:    maxLatency / 2,
		hardCap:    maxLatency * 3 / 4,
		maxLatency: maxLatency,
	}
}

// Tier returns the current tier based on the latest observed effective drift.
func (tc *Controller) Tier() Tier {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.tier
}

// OnTierChange sets the callback. If the tier is already above TierNormal,
// it's invoked immediately with the current tier.
func (tc *Controller) OnTierChange(cb func(Tier)) {
	tc.mu.Lock()
	tc.tierCB = cb
	cur := tc.tier
	tc.mu.Unlock()

	if cb != nil && cur != TierNormal {
		cb(cur)
	}
}

// tierFor returns the tier for the given effective drift. Caller-side helper;
// does not touch state.
func (tc *Controller) tierFor(effective time.Duration) Tier {
	abs := effective.Abs()
	switch {
	case abs >= tc.hardCap:
		return TierHard
	case abs >= tc.softCap:
		return TierSoft
	default:
		return TierNormal
	}
}

// updateTierLocked recomputes the tier from effective drift and returns
// (changed, newTier, callback). Caller must hold tc.mu and invoke the callback
// after releasing it.
func (tc *Controller) updateTierLocked(effective time.Duration) (bool, Tier, func(Tier)) {
	next := tc.tierFor(effective)
	if next == tc.tier {
		return false, next, nil
	}
	tc.tier = next
	return true, next, tc.tierCB
}

// SetDrift updates the current observed drift. If no correction is active
// and the effective (uncorrected) drift exceeds the threshold, a new
// correction is started.
func (tc *Controller) SetDrift(drift time.Duration) {
	tc.mu.Lock()
	tc.drift = drift

	effective := drift - tc.corrected

	var toStart time.Duration
	if tc.current == 0 {
		if effective.Abs() >= DefaultThreshold {
			toStart = effective
			tc.current = effective
		}
	}
	tierChanged, newTier, tierCB := tc.updateTierLocked(effective)
	cb := tc.cb
	tc.mu.Unlock()

	if toStart != 0 && cb != nil {
		cb(toStart)
	}
	if tierChanged && tierCB != nil {
		tierCB(newTier)
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
	tierChanged, newTier, tierCB := tc.updateTierLocked(effective)
	cb := tc.cb
	tc.mu.Unlock()

	if toStart != 0 && cb != nil {
		cb(toStart)
	}
	if tierChanged && tierCB != nil {
		tierCB(newTier)
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
