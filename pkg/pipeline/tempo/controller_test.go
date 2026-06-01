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

func TestOvershootDoesNotFlipDirection(t *testing.T) {
	// Probe trips at first buffer past target; reported actual will overshoot.
	// Controller must not interpret that as a sign reversal and counter-correct.
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.SetDrift(15 * time.Millisecond) // starts 15ms correction
	tc.DriftProcessed(25 * time.Millisecond) // overshoots target by 10ms

	if len(calls) != 1 {
		t.Fatalf("overshoot must not trigger counter-correction: got %v", calls)
	}
	if got := tc.Processed(); got != 15*time.Millisecond {
		t.Fatalf("corrected should be clamped to drift on overshoot: got %v, want 15ms", got)
	}
}

func TestOvershootThenDriftReverses(t *testing.T) {
	// After overshoot clamps corrected to drift, a real reversal must still fire.
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.SetDrift(15 * time.Millisecond)
	tc.DriftProcessed(25 * time.Millisecond) // overshoot, clamped
	tc.SetDrift(-20 * time.Millisecond)      // real reversal

	if len(calls) != 2 || calls[1] != -35*time.Millisecond {
		t.Fatalf("reversal after overshoot: got %v, want [15ms, -35ms]", calls)
	}
}

func TestNegativeOvershootDoesNotFlipDirection(t *testing.T) {
	tc := NewController()

	var calls []time.Duration
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })

	tc.SetDrift(-15 * time.Millisecond)
	tc.DriftProcessed(-25 * time.Millisecond) // overshoot in negative direction

	if len(calls) != 1 {
		t.Fatalf("negative overshoot must not trigger counter-correction: got %v", calls)
	}
	if got := tc.Processed(); got != -15*time.Millisecond {
		t.Fatalf("corrected should be clamped to drift on overshoot: got %v, want -15ms", got)
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

func TestCancelInFlightClearsCurrent(t *testing.T) {
	// CancelInFlight must clear `current` so a subsequent
	// OnDriftDetectedCallback registration does not re-fire the abandoned
	// target. This is the load-bearing property for the source-bin-reset
	// path: when the audio downstream of the pacer is discarded, the partial
	// compensation cannot be re-applied by a fresh pacer with the same target.
	tc := NewController()

	cb1Calls := []time.Duration{}
	tc.OnDriftDetectedCallback(func(d time.Duration) { cb1Calls = append(cb1Calls, d) })
	tc.SetDrift(20 * time.Millisecond) // arms correction, fires cb1 with 20ms

	if len(cb1Calls) != 1 || cb1Calls[0] != 20*time.Millisecond {
		t.Fatalf("expected initial arm: got %v", cb1Calls)
	}

	// Detach and cancel — simulates resetAudioAppSrcBin tearing down the old
	// pacer mid-correction.
	tc.OnDriftDetectedCallback(nil)
	tc.CancelInFlight()

	// Register a new callback. Without CancelInFlight, this would re-fire
	// with the abandoned 20ms target and double-correct.
	cb2Calls := []time.Duration{}
	tc.OnDriftDetectedCallback(func(d time.Duration) { cb2Calls = append(cb2Calls, d) })

	if len(cb2Calls) != 0 {
		t.Fatalf("new callback must not re-fire abandoned target after CancelInFlight: got %v", cb2Calls)
	}

	// corrected is unchanged — prior completed corrections still on the books.
	if got := tc.Processed(); got != 0 {
		t.Fatalf("CancelInFlight must not credit any compensation: Processed got %v, want 0", got)
	}

	// A fresh SR that still shows drift above threshold should arm fresh.
	tc.SetDrift(30 * time.Millisecond)
	if len(cb2Calls) != 1 || cb2Calls[0] != 30*time.Millisecond {
		t.Fatalf("post-cancel SetDrift must arm fresh: got %v", cb2Calls)
	}
}

func TestCancelInFlightWhenIdleIsNoop(t *testing.T) {
	// Calling CancelInFlight when no correction is in flight must not corrupt
	// state. resetAudioAppSrcBin calls it unconditionally.
	tc := NewController()

	tc.CancelInFlight()
	if got := tc.Processed(); got != 0 {
		t.Fatalf("Processed should be 0 after idle CancelInFlight, got %v", got)
	}

	calls := []time.Duration{}
	tc.OnDriftDetectedCallback(func(d time.Duration) { calls = append(calls, d) })
	tc.SetDrift(20 * time.Millisecond)
	if len(calls) != 1 || calls[0] != 20*time.Millisecond {
		t.Fatalf("controller must still arm after idle CancelInFlight: got %v", calls)
	}
}
