// Copyright 2023 LiveKit, Inc.
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

package stats

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"

	"github.com/livekit/egress/pkg/config"
)

func TestCostsForRequest(t *testing.T) {
	m := &Monitor{
		cpuCostConfig: &config.CPUCostConfig{
			RoomCompositeCpuCost:            4,
			AudioRoomCompositeCpuCost:       1,
			SDKAudioRoomCompositeCpuCost:    0.5,
			SDKAudioRoomCompositeMemoryCost: 1,
			WebCpuCost:                      4.5,
			AudioWebCpuCost:                 1.5,
			ParticipantCpuCost:              2,
			TrackCompositeCpuCost:           2.5,
			TrackCpuCost:                    0.2,
			MemoryCost:                      3,
		},
	}

	roomComposite := func(r *livekit.RoomCompositeEgressRequest) *rpc.StartEgressRequest {
		return &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_RoomComposite{RoomComposite: r}}
	}
	v2Template := func(tmpl *livekit.TemplateSource) *rpc.StartEgressRequest {
		return &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_Egress{
			Egress: &livekit.StartEgressRequest{Source: &livekit.StartEgressRequest_Template{Template: tmpl}},
		}}
	}

	for _, tc := range []struct {
		name   string
		req    *rpc.StartEgressRequest
		cpu    float64
		memory float64
		isWeb  bool
	}{
		{
			name: "room composite video",
			req:  roomComposite(&livekit.RoomCompositeEgressRequest{}),
			cpu:  4, memory: 3, isWeb: true,
		},
		{
			name: "room composite audio sdk",
			req:  roomComposite(&livekit.RoomCompositeEgressRequest{AudioOnly: true}),
			cpu:  0.5, memory: 1, isWeb: false,
		},
		{
			name: "room composite audio with layout",
			req:  roomComposite(&livekit.RoomCompositeEgressRequest{AudioOnly: true, Layout: "speaker"}),
			cpu:  1, memory: 3, isWeb: true,
		},
		{
			name: "room composite audio with custom base url",
			req:  roomComposite(&livekit.RoomCompositeEgressRequest{AudioOnly: true, CustomBaseUrl: "https://example.com"}),
			cpu:  1, memory: 3, isWeb: true,
		},
		{
			name: "web",
			req: &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_Web{
				Web: &livekit.WebEgressRequest{},
			}},
			cpu: 4.5, memory: 3, isWeb: true,
		},
		{
			name: "web audio only",
			req: &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_Web{
				Web: &livekit.WebEgressRequest{AudioOnly: true},
			}},
			cpu: 1.5, memory: 3, isWeb: true,
		},
		{
			name: "participant",
			req: &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_Participant{
				Participant: &livekit.ParticipantEgressRequest{},
			}},
			cpu: 2, memory: 3, isWeb: false,
		},
		{
			name: "track composite",
			req: &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_TrackComposite{
				TrackComposite: &livekit.TrackCompositeEgressRequest{},
			}},
			cpu: 2.5, memory: 3, isWeb: false,
		},
		{
			name: "track",
			req: &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_Track{
				Track: &livekit.TrackEgressRequest{},
			}},
			cpu: 0.2, memory: 3, isWeb: false,
		},
		{
			name: "v2 template audio sdk",
			req:  v2Template(&livekit.TemplateSource{AudioOnly: true}),
			cpu:  0.5, memory: 1, isWeb: false,
		},
		{
			name: "v2 template audio with layout",
			req:  v2Template(&livekit.TemplateSource{AudioOnly: true, Layout: "speaker"}),
			cpu:  1, memory: 3, isWeb: true,
		},
		{
			name: "v2 template audio with custom base url",
			req:  v2Template(&livekit.TemplateSource{AudioOnly: true, CustomBaseUrl: "https://example.com"}),
			cpu:  1, memory: 3, isWeb: true,
		},
		{
			name: "v2 template video",
			req:  v2Template(&livekit.TemplateSource{}),
			cpu:  4, memory: 3, isWeb: true,
		},
		{
			name: "v2 web",
			req: &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_Egress{
				Egress: &livekit.StartEgressRequest{Source: &livekit.StartEgressRequest_Web{Web: &livekit.WebSource{}}},
			}},
			cpu: 4.5, memory: 3, isWeb: true,
		},
		{
			name: "v2 media",
			req: &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_Egress{
				Egress: &livekit.StartEgressRequest{Source: &livekit.StartEgressRequest_Media{Media: &livekit.MediaSource{}}},
			}},
			cpu: 2, memory: 3, isWeb: false,
		},
		{
			name: "v2 replay template audio sdk",
			req: &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_Replay{
				Replay: &livekit.ExportReplayRequest{
					Source: &livekit.ExportReplayRequest_Template{Template: &livekit.TemplateSource{AudioOnly: true}},
				},
			}},
			cpu: 0.5, memory: 1, isWeb: false,
		},
		{
			name: "v2 no source",
			req: &rpc.StartEgressRequest{Request: &rpc.StartEgressRequest_Egress{
				Egress: &livekit.StartEgressRequest{},
			}},
			cpu: 0, memory: 3, isWeb: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			costs := m.costsForRequest(tc.req)
			require.Equal(t, tc.cpu, costs.cpu, "cpu")
			require.Equal(t, tc.memory, costs.memory, "memory")
			require.Equal(t, tc.isWeb, costs.isWeb, "isWeb")
		})
	}
}
