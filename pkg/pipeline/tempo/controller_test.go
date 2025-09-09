package tempo

import (
	"testing"
	"time"
)

func TestEnqueueStartsWithinBudget(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.EnqueueDrift(30 * time.Millisecond) // > threshold, under budget
	if len(calls) != 1 || calls[0] != 30*time.Millisecond {
		t.Fatalf("callback: got %v, want [30ms]", calls)
	}
	if got := tc.Processed(); got != 0 {
		t.Fatalf("processed before DriftProcessed: got %v, want 0", got)
	}
}

func TestThresholdAccumulation(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.EnqueueDrift(5 * time.Millisecond) // below threshold → no start
	tc.EnqueueDrift(6 * time.Millisecond) // total 11ms → start now

	if len(calls) != 1 || calls[0] != 11*time.Millisecond {
		t.Fatalf("callback: got %v, want [11ms]", calls)
	}
}

func TestDriftProcessedStartsNext(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.EnqueueDrift(30 * time.Millisecond) // starts immediately
	if len(calls) != 1 || calls[0] != 30*time.Millisecond {
		t.Fatalf("first start: got %v", calls)
	}

	tc.EnqueueDrift(40 * time.Millisecond) // pending, not started yet
	if len(calls) != 1 {
		t.Fatalf("should not start second yet: got %v", calls)
	}

	tc.DriftProcessed() // finish first → second starts
	if len(calls) != 2 || calls[1] != 40*time.Millisecond {
		t.Fatalf("second start: got %v", calls)
	}

	if got := tc.Processed(); got != 30*time.Millisecond {
		t.Fatalf("processed after first completion: got %v, want 30ms", got)
	}
}

func TestBudgetBlocksAndResumes(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	// Spend most of the budget (1.9s)
	tc.EnqueueDrift(1900 * time.Millisecond)
	if len(calls) != 1 || calls[0] != 1900*time.Millisecond {
		t.Fatalf("start 1.9s: got %v", calls)
	}
	tc.DriftProcessed()
	if got := tc.Processed(); got != 1900*time.Millisecond {
		t.Fatalf("processed after 1.9s: got %v", got)
	}

	// +300ms would exceed 2s budget → must NOT start
	tc.EnqueueDrift(300 * time.Millisecond)
	if len(calls) != 1 {
		t.Fatalf("over-budget should not start: got %v", calls)
	}

	// Add -650ms → pending becomes -350ms → net processed+pending = 1.55s → start
	tc.EnqueueDrift(-650 * time.Millisecond)
	if len(calls) != 2 || calls[1] != -350*time.Millisecond {
		t.Fatalf("start -350ms: got %v", calls)
	}
	tc.DriftProcessed()
	if got := tc.Processed(); got != (1900*time.Millisecond - 350*time.Millisecond) {
		t.Fatalf("processed signed total: got %v, want 1.55s", got)
	}
}

func TestImmediateCallbackOnRegister(t *testing.T) {
	tc := NewController()

	// Arm a correction before registering callback
	tc.EnqueueDrift(20 * time.Millisecond)

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	// Should fire immediately with current
	if len(calls) != 1 || calls[0] != 20*time.Millisecond {
		t.Fatalf("immediate callback: got %v, want [20ms]", calls)
	}
}

func TestZeroDriftNoop(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.EnqueueDrift(0)
	if len(calls) != 0 {
		t.Fatalf("zero drift should do nothing, got %v", calls)
	}
}

func TestSignedProcessedAccumulation(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.EnqueueDrift(30 * time.Millisecond)
	tc.DriftProcessed()
	if got := tc.Processed(); got != 30*time.Millisecond {
		t.Fatalf("after +30ms processed: got %v", got)
	}

	tc.EnqueueDrift(-10 * time.Millisecond)
	tc.DriftProcessed()
	if got := tc.Processed(); got != 20*time.Millisecond {
		t.Fatalf("after +30-10 processed: got %v, want 20ms", got)
	}
}
