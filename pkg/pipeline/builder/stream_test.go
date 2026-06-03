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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvaluateLiveness(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name           string
		current        uint64
		last           uint64
		lastProgressAt time.Time
		want           livenessAction
	}{
		{
			name:           "real progress: growth well above handshake threshold",
			current:        100_000,
			last:           50_000,
			lastProgressAt: now.Add(-1 * time.Second),
			want:           livenessActionRealProgress,
		},
		{
			name:           "real progress: very first tick with substantial outBytes",
			current:        50_000,
			last:           0,
			lastProgressAt: now.Add(-1 * time.Second),
			want:           livenessActionRealProgress,
		},
		{
			name:           "small handshake-era noise does not count as progress",
			current:        300,
			last:           0,
			lastProgressAt: now.Add(-5 * time.Second),
			want:           livenessActionNone,
		},
		{
			// A full reconnect-attempt's worth of chunk control traffic
			// (connect/releaseStream/FCPublish/createStream/publish + bandwidth
			// chunks) is typically ~500B–1.5KB; even 2 KB is on the high end and
			// must not register as real streaming progress.
			name:           "reconnect-control-message budget does not cross threshold",
			current:        2 * 1024,
			last:           0,
			lastProgressAt: now.Add(-5 * time.Second),
			want:           livenessActionNone,
		},
		{
			name:           "growth exactly at threshold is not progress",
			current:        4 * 1024,
			last:           0,
			lastProgressAt: now.Add(-5 * time.Second),
			want:           livenessActionNone,
		},
		{
			name:           "growth comfortably over threshold counts as progress",
			current:        8 * 1024,
			last:           0,
			lastProgressAt: now.Add(-5 * time.Second),
			want:           livenessActionRealProgress,
		},
		{
			name:           "no growth, within idle window",
			current:        500,
			last:           500,
			lastProgressAt: now.Add(-10 * time.Second),
			want:           livenessActionNone,
		},
		{
			name:           "no growth, past idle window: fail",
			current:        500,
			last:           500,
			lastProgressAt: now.Add(-31 * time.Second),
			want:           livenessActionFail,
		},
		{
			// Stream was healthy for a while (lastProgressAt was advanced when
			// real growth was observed earlier), then writes plateaued — e.g.
			// the sink wedged with bytes already buffered. After idle timeout
			// from the last real-progress mark, the monitor must fail.
			name:           "stalled after prior real progress fails after timeout",
			current:        2_000_000,
			last:           2_000_000,
			lastProgressAt: now.Add(-31 * time.Second),
			want:           livenessActionFail,
		},
		{
			name:           "stats reset during reconnect (current < last) is not progress and not failure",
			current:        0,
			last:           50_000,
			lastProgressAt: now.Add(-31 * time.Second),
			want:           livenessActionNone,
		},
		{
			name:           "current==0 always defers to error path, even past idle window",
			current:        0,
			last:           0,
			lastProgressAt: now.Add(-1 * time.Hour),
			want:           livenessActionNone,
		},
		{
			name:           "sub-threshold growth past idle window: fail (handshake-only loop)",
			current:        300,
			last:           200,
			lastProgressAt: now.Add(-31 * time.Second),
			want:           livenessActionFail,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateLiveness(tc.current, tc.last, tc.lastProgressAt, now)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestApplyLiveness_RealProgressResetsRetryBudget(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)

	s := &Stream{}
	s.lastProgressAt.Store(now.Add(-10 * time.Second))
	s.disconnectedAt.Store(now.Add(-5 * time.Second))
	s.reconnections.Store(3)

	s.applyLiveness(50_000, now)

	require.Equal(t, uint64(50_000), s.lastOutBytesTotal.Load())
	require.Equal(t, now.UnixNano(), s.lastProgressAt.Load().UnixNano())
	require.True(t, s.disconnectedAt.Load().IsZero(), "disconnectedAt should be cleared")
	require.Equal(t, int32(0), s.reconnections.Load(), "reconnections should be reset")
	require.True(t, s.monitorObservedProgress.Load(), "real progress must set the observed-progress flag")
	require.False(t, s.livenessFailed.Load())
}

func TestApplyLiveness_HandshakeBytesDoNotResetRetryBudget(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)

	s := &Stream{}
	s.lastProgressAt.Store(now.Add(-10 * time.Second))
	disconnected := now.Add(-5 * time.Second)
	s.disconnectedAt.Store(disconnected)
	s.reconnections.Store(3)

	// 300 bytes = typical RTMP handshake/control noise; must not look like progress.
	s.applyLiveness(300, now)

	require.Equal(t, uint64(300), s.lastOutBytesTotal.Load())
	require.Equal(t, disconnected.UnixNano(), s.disconnectedAt.Load().UnixNano(),
		"disconnectedAt must not be cleared by sub-threshold growth")
	require.Equal(t, int32(3), s.reconnections.Load(),
		"reconnections must not be reset by sub-threshold growth")
	require.False(t, s.monitorObservedProgress.Load(),
		"sub-threshold growth must not set the observed-progress flag")
	require.False(t, s.livenessFailed.Load())
}

func TestApplyLiveness_StatsResetDoesNotExtendClock(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)

	s := &Stream{}
	originalProgressAt := now.Add(-20 * time.Second)
	s.lastProgressAt.Store(originalProgressAt)
	s.lastOutBytesTotal.Store(50_000)

	// Simulate SetState(NULL) clearing the sink stats.
	s.applyLiveness(0, now)

	require.Equal(t, uint64(0), s.lastOutBytesTotal.Load(), "baseline must follow the reset")
	require.Equal(t, originalProgressAt.UnixNano(), s.lastProgressAt.Load().UnixNano(),
		"lastProgressAt must not advance on a stats reset")
	require.False(t, s.livenessFailed.Load())
}

// TestApplyLiveness_ObservedProgressIsSticky exercises the slow-acking-server
// scenario the integration test uncovered: a server that successfully receives
// media but doesn't promptly send RTMP Acknowledgements. The monitor's view
// (driven by OutBytesTotal) sees real growth; once that flag is set, it stays
// set across subsequent ticks even if growth stalls — so Reset's bad-URL
// fast-fail check can still classify a later disconnect as "we were streaming."
func TestApplyLiveness_ObservedProgressIsSticky(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)

	s := &Stream{}
	s.lastProgressAt.Store(now.Add(-1 * time.Second))

	// Tick 1 — real growth → flag set.
	s.applyLiveness(100_000, now)
	require.True(t, s.monitorObservedProgress.Load())

	// Tick 2 — no growth. Flag must remain set.
	s.applyLiveness(100_000, now.Add(5*time.Second))
	require.True(t, s.monitorObservedProgress.Load(),
		"monitorObservedProgress must not be cleared once set")

	// Tick 3 — stats reset (SetState NULL during reconnect). Flag must remain set.
	s.applyLiveness(0, now.Add(10*time.Second))
	require.True(t, s.monitorObservedProgress.Load(),
		"monitorObservedProgress must survive a stats reset")
}
