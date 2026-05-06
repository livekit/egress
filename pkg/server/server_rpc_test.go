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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
)

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
