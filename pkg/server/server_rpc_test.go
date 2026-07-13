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

package server

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"

	"github.com/livekit/egress/pkg/config"
)

// TestConsumePendingClaim_FloorAtZero verifies the CAS loop never decrements below zero.
func TestConsumePendingClaim_FloorAtZero(t *testing.T) {
	s := &Server{}

	// Counter starts at 0; extra consume calls must be no-ops.
	s.consumePendingClaim()
	s.consumePendingClaim()
	require.Equal(t, int32(0), s.pendingClaims.Load())

	// One increment; one consume; counter back to 0.
	s.pendingClaims.Inc()
	require.Equal(t, int32(1), s.pendingClaims.Load())
	s.consumePendingClaim()
	require.Equal(t, int32(0), s.pendingClaims.Load())

	// Second consume after counter is already 0 is a no-op.
	s.consumePendingClaim()
	require.Equal(t, int32(0), s.pendingClaims.Load())
}

// TestConsumePendingClaim_NoDoubleDecrement verifies that concurrent consumptions
// of the same claim (StartEgress + self-decay timer racing) each fire exactly once
// and together decrement the counter by exactly N, not 2N.
func TestConsumePendingClaim_NoDoubleDecrement(t *testing.T) {
	const claims = 50
	s := &Server{}

	for i := 0; i < claims; i++ {
		s.pendingClaims.Inc()
	}
	require.Equal(t, int32(claims), s.pendingClaims.Load())

	// Simulate StartEgress and self-decay racing concurrently for all claims.
	// Both sides try to decrement; together they must not go below zero.
	var wg sync.WaitGroup
	for i := 0; i < claims*2; i++ { // 2× concurrency, one genuine + one spurious per claim
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.consumePendingClaim()
		}()
	}
	wg.Wait()

	require.Equal(t, int32(0), s.pendingClaims.Load(), "counter must be exactly 0, not negative")
}

// --- Tests for softRejectScore (TASK-03) ---

func TestSoftRejectFloorDisabled(t *testing.T) {
	// SoftRejectFloor=0 → feature disabled → always return -1
	s := &Server{conf: &config.ServiceConfig{SoftRejectFloor: 0}}
	require.Equal(t, float32(-1), s.softRejectScore())
}

func TestSoftRejectFloorReturnedWhenBelowMax(t *testing.T) {
	// Floor set, activeRequests < MaxActiveRequests → return floor
	s := &Server{conf: &config.ServiceConfig{SoftRejectFloor: 0.01, MaxActiveRequests: 16}}
	s.activeRequests.Store(8)
	require.Equal(t, float32(0.01), s.softRejectScore())
}

func TestSoftRejectFloorHardRejectWhenAtMax(t *testing.T) {
	// Floor set, activeRequests >= MaxActiveRequests → -1 (genuinely full)
	s := &Server{conf: &config.ServiceConfig{SoftRejectFloor: 0.01, MaxActiveRequests: 16}}
	s.activeRequests.Store(16)
	require.Equal(t, float32(-1), s.softRejectScore())
}

func TestSoftRejectFloorNoGuardWhenMaxIsZero(t *testing.T) {
	// MaxActiveRequests=0 → guard disabled → return floor regardless of load
	s := &Server{conf: &config.ServiceConfig{SoftRejectFloor: 0.01, MaxActiveRequests: 0}}
	s.activeRequests.Store(20)
	require.Equal(t, float32(0.01), s.softRejectScore())
}


func TestIsHeavyEgressRequest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		req      *rpc.StartEgressRequest
		expected bool
	}{
		{
			name:     "RoomComposite is heavy",
			req:      &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_RoomComposite{RoomComposite: &livekit.RoomCompositeEgressRequest{}}},
			expected: true,
		},
		{
			name:     "Web is heavy",
			req:      &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_Web{Web: &livekit.WebEgressRequest{}}},
			expected: true,
		},
		{
			name:     "Track is not heavy",
			req:      &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_Track{Track: &livekit.TrackEgressRequest{}}},
			expected: false,
		},
		{
			name:     "TrackComposite is not heavy",
			req:      &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_TrackComposite{TrackComposite: &livekit.TrackCompositeEgressRequest{}}},
			expected: false,
		},
		{
			name:     "Participant is not heavy",
			req:      &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_Participant{Participant: &livekit.ParticipantEgressRequest{}}},
			expected: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, isHeavyEgressRequest(tc.req))
		})
	}
}
