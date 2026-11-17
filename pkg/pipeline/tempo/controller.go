package tempo

import (
	"time"

	"github.com/linkdata/deadlock"

	"github.com/livekit/protocol/logger"
)

const (
	// DefaultThreshold is the minimum effective drift before a correction starts.
	DefaultThreshold = 10 * time.Millisecond

	maxResets = 2
)

// Tier indicates how far effective drift has grown relative to the configured
// budgets. The pacer / caller wires escalation behavior to tier transitions.
type Tier int

const (
	TierNormal Tier = iota // |effective drift| < soft budget
	TierSoft               // soft budget exceeded; pacer should boost
	TierHard               // hard budget exceeded; caller should reset
)

// Stats summarizes a track's tempo correction over the whole egress.
type Stats struct {
	Applied     time.Duration
	Corrections int
	Resets      int
	Disabled    bool
}

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

	warnShift     time.Duration
	actShift      time.Duration
	shiftWarned   bool
	resetPending  bool // until pitch sees the flush, shift and drift still describe the discarded timeline
	dropNextDrift bool // the SDK can deliver a report sampled before the re-anchor after the flush
	resets        int
	disabled      bool
	applied       time.Duration // lifetime net correction; reset leaves it
	corrections   int

	cb      func(time.Duration) // invoked with correction amount to apply
	tierCB  func(Tier)          // invoked on tier transitions
	resetCB func()

	logger logger.Logger
}

func NewController(maxLatency time.Duration) *Controller {
	return &Controller{
		softCap:    maxLatency / 2,
		hardCap:    maxLatency * 3 / 4,
		maxLatency: maxLatency,
		warnShift:  maxLatency / 3,
		actShift:   maxLatency / 2,
		logger:     logger.GetLogger(),
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
	if tc.resetPending || tc.dropNextDrift {
		tc.dropNextDrift = false
		tc.mu.Unlock()
		return
	}
	tc.drift = drift
	if tc.disabled {
		tc.mu.Unlock()
		return
	}

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
	tc.applied += actual
	tc.corrections++
	tc.current = 0
	if tc.resetPending {
		tc.mu.Unlock()
		return
	}
	tc.corrected += actual
	if tc.disabled {
		tc.mu.Unlock()
		return
	}

	// actual is the stretch the pacer applied, so an overshoot past drift makes the
	// next correction smaller, or starts a counter-correction once it reaches the threshold.
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

// SetLogger sets the logger used for drift warnings and resets.
func (tc *Controller) SetLogger(l logger.Logger) {
	tc.mu.Lock()
	tc.logger = l
	tc.mu.Unlock()
}

// OnReset sets the callback fired after a reset; without one the controller never resets.
func (tc *Controller) OnReset(cb func()) {
	tc.mu.Lock()
	tc.resetCB = cb
	tc.mu.Unlock()
}

// ObserveOutputShift takes pitch's output end minus its input end, warning past warnShift and resetting past actShift.
func (tc *Controller) ObserveOutputShift(shift time.Duration) {
	tc.mu.Lock()
	// on the legacy synchronizer, input PTS are on the publisher clock, so the shift is the correction itself
	if tc.resetCB == nil || tc.resetPending || shift.Abs() < tc.warnShift {
		tc.mu.Unlock()
		return
	}
	if shift.Abs() >= tc.actShift {
		tc.resetLocked("output shift", shift)
		return
	}

	warn := !tc.shiftWarned
	tc.shiftWarned = true
	corrected, resets, l := tc.corrected, tc.resets, tc.logger
	tc.mu.Unlock()

	if warn {
		l.Warnw("audio output drifting from its input timeline", nil,
			"shift", shift, "corrected", corrected, "resets", resets)
	}
}

// ResetHardTier discards the corrections when effective drift reaches the hard budget.
func (tc *Controller) ResetHardTier() {
	tc.mu.Lock()
	if tc.resetCB == nil || tc.disabled || tc.resetPending {
		tc.mu.Unlock()
		return
	}
	tc.resetLocked("hard tier", tc.drift-tc.corrected)
}

// Flushed tells the controller that pitch has seen a flush, ending any pending reset.
func (tc *Controller) Flushed() {
	tc.mu.Lock()
	if tc.resetPending {
		tc.resetPending = false
		tc.dropNextDrift = true
	}
	tc.mu.Unlock()
}

// Stats returns the correction summary for the track's end stats.
func (tc *Controller) Stats() Stats {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return Stats{
		Applied:     tc.applied,
		Corrections: tc.corrections,
		Resets:      tc.resets,
		Disabled:    tc.disabled,
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

// resetLocked must be called with tc.mu held; it releases it before logging and firing the callback.
func (tc *Controller) resetLocked(reason string, measured time.Duration) {
	discarded := tc.corrected
	tc.drift = 0
	tc.corrected = 0
	tc.current = 0
	tc.tier = TierNormal
	tc.shiftWarned = false
	tc.resetPending = true
	tc.resets++
	if tc.resets >= maxResets {
		tc.disabled = true
	}
	resets, disabled, cb, l := tc.resets, tc.disabled, tc.resetCB, tc.logger
	tc.mu.Unlock()

	l.Warnw("resetting audio tempo correction", nil,
		"reason", reason,
		"measured", measured,
		"discardedCorrection", discarded,
		"resets", resets,
		"tempoDisabled", disabled,
	)
	cb()
}
