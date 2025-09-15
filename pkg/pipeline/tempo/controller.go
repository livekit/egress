package tempo

import (
	"time"

	"github.com/linkdata/deadlock"
)

const (
	DefaultThreshold = 10 * time.Millisecond // don’t start tiny corrections
	MaxDriftBudget   = 2 * time.Second       // cap on processed drift magnitude
)

type Controller struct {
	mu deadlock.Mutex

	pending   time.Duration // accumulated, not yet started
	current   time.Duration // currently being corrected
	processed time.Duration // signed sum of ALL corrections already applied

	cb func(time.Duration) // invoked with the next 'current' to apply
}

func NewController() *Controller { return &Controller{} }

// EnqueueDrift adds signed drift. It may synchronously arm a new correction
// if idle, above threshold, and starting it would not exceed the budget.
func (tc *Controller) EnqueueDrift(drift time.Duration) {
	if drift == 0 {
		return
	}

	tc.mu.Lock()
	tc.pending += drift

	var toStart time.Duration
	if tc.current == 0 && tc.pending.Abs() >= DefaultThreshold {
		// Only start if applying 'pending' keeps processed within budget.
		if (tc.processed + tc.pending).Abs() < MaxDriftBudget {
			toStart = tc.pending
			tc.current = toStart
			tc.pending = 0
		}
	}
	cb := tc.cb
	tc.mu.Unlock()

	if toStart != 0 && cb != nil {
		cb(toStart)
	}
}

// DriftProcessed marks the *current* correction as finished and may start the next
// one if available and within budget.
func (tc *Controller) DriftProcessed() {
	tc.mu.Lock()
	tc.processed += tc.current
	tc.current = 0

	var toStart time.Duration
	if tc.pending.Abs() >= DefaultThreshold && (tc.processed+tc.pending).Abs() < MaxDriftBudget {
		toStart = tc.pending
		tc.current = toStart
		tc.pending = 0
	}
	cb := tc.cb
	tc.mu.Unlock()

	if toStart != 0 && cb != nil {
		cb(toStart)
	}
}

// OnDriftDetectedCallback sets the callback. If a correction is already armed,
// it’s invoked immediately with that value.
func (tc *Controller) OnDriftDetectedCallback(cb func(time.Duration)) {
	tc.mu.Lock()
	tc.cb = cb
	cur := tc.current
	tc.mu.Unlock()

	if cb != nil && cur != 0 {
		cb(cur)
	}
}

// Processed returns the total of already-applied corrections.
func (tc *Controller) Processed() time.Duration {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.processed
}
