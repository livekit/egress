package tempo

import (
	"testing"
	"time"
)

func TestSetDriftStartsAboveThreshold(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.SetDrift(30 * time.Millisecond) // > threshold
	if len(calls) != 1 || calls[0] != 30*time.Millisecond {
		t.Fatalf("callback: got %v, want [30ms]", calls)
	}
	if got := tc.Processed(); got != 0 {
		t.Fatalf("processed before DriftProcessed: got %v, want 0", got)
	}
}

func TestBelowThresholdNoop(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.SetDrift(5 * time.Millisecond) // below threshold
	if len(calls) != 0 {
		t.Fatalf("should not start below threshold: got %v", calls)
	}
}

func TestThresholdCrossing(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.SetDrift(5 * time.Millisecond)  // below threshold
	tc.SetDrift(12 * time.Millisecond) // above threshold

	if len(calls) != 1 || calls[0] != 12*time.Millisecond {
		t.Fatalf("callback: got %v, want [12ms]", calls)
	}
}

func TestNoNewCorrectionWhileActive(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.SetDrift(30 * time.Millisecond) // starts correction
	tc.SetDrift(50 * time.Millisecond) // updates drift but doesn't start new correction

	if len(calls) != 1 {
		t.Fatalf("should not start second while active: got %v", calls)
	}
}

func TestDriftProcessedStartsNext(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.SetDrift(30 * time.Millisecond) // starts 30ms correction
	tc.SetDrift(50 * time.Millisecond) // drift grew while correcting

	tc.DriftProcessed(30 * time.Millisecond) // finishes 30ms → effective = 50-30 = 20ms → starts 20ms

	if len(calls) != 2 || calls[1] != 20*time.Millisecond {
		t.Fatalf("second correction: got %v, want [30ms, 20ms]", calls)
	}

	if got := tc.Processed(); got != 30*time.Millisecond {
		t.Fatalf("processed after first: got %v, want 30ms", got)
	}
}

func TestDriftProcessedNoFollowUp(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.SetDrift(30 * time.Millisecond)
	tc.DriftProcessed(30 * time.Millisecond) // effective = 30-30 = 0 → no follow-up

	if len(calls) != 1 {
		t.Fatalf("should not start follow-up at zero drift: got %v", calls)
	}
	if got := tc.Processed(); got != 30*time.Millisecond {
		t.Fatalf("processed: got %v, want 30ms", got)
	}
}

func TestNegativeDrift(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.SetDrift(-25 * time.Millisecond)
	if len(calls) != 1 || calls[0] != -25*time.Millisecond {
		t.Fatalf("callback: got %v, want [-25ms]", calls)
	}
}

func TestOngoingDriftCorrection(t *testing.T) {
	// Simulate clock skew: drift grows by 10ms per SR
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.SetDrift(10 * time.Millisecond) // starts 10ms correction
	tc.SetDrift(20 * time.Millisecond) // drift grew

	tc.DriftProcessed(10 * time.Millisecond) // effective = 20-10 = 10ms → starts 10ms
	if len(calls) != 2 || calls[1] != 10*time.Millisecond {
		t.Fatalf("second correction: got %v", calls)
	}

	tc.SetDrift(30 * time.Millisecond) // drift grew again
	tc.DriftProcessed(10 * time.Millisecond) // effective = 30-20 = 10ms → starts 10ms
	if len(calls) != 3 || calls[2] != 10*time.Millisecond {
		t.Fatalf("third correction: got %v", calls)
	}

	if got := tc.Processed(); got != 20*time.Millisecond {
		t.Fatalf("processed: got %v, want 20ms", got)
	}
}

func TestImmediateCallbackOnRegister(t *testing.T) {
	tc := NewController()

	// Arm a correction before registering callback
	tc.SetDrift(20 * time.Millisecond)

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

	tc.SetDrift(0)
	if len(calls) != 0 {
		t.Fatalf("zero drift should do nothing, got %v", calls)
	}
}
