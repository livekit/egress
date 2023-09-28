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

//go:build integration

package test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
)

func (r *Runner) runStreamTest(t *testing.T, req *rpc.StartEgressRequest, test *testCase) {
	ctx := context.Background()

	egressID := r.startEgress(t, req)

	// get params
	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)
	require.Equal(t, test.expectVideoEncoding, p.VideoEncoding)

	// verify and check update
	time.Sleep(time.Second * 5)
	r.verifyStreams(t, p, streamUrl1)
	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		redactedUrl1:    livekit.StreamInfo_ACTIVE,
		redactedBadUrl1: livekit.StreamInfo_FAILED,
	})

	// add one good stream url and one bad
	_, err = r.client.UpdateStream(ctx, egressID, &livekit.UpdateStreamRequest{
		EgressId:      egressID,
		AddOutputUrls: []string{badStreamUrl2, streamUrl2},
	})
	require.NoError(t, err)

	// verify and check updates
	time.Sleep(time.Second * 5)
	r.verifyStreams(t, p, streamUrl1, streamUrl2)

	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		redactedUrl1:    livekit.StreamInfo_ACTIVE,
		redactedUrl2:    livekit.StreamInfo_ACTIVE,
		redactedBadUrl1: livekit.StreamInfo_FAILED,
		redactedBadUrl2: livekit.StreamInfo_ACTIVE,
	})
	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		redactedUrl1:    livekit.StreamInfo_ACTIVE,
		redactedUrl2:    livekit.StreamInfo_ACTIVE,
		redactedBadUrl1: livekit.StreamInfo_FAILED,
		redactedBadUrl2: livekit.StreamInfo_FAILED,
	})

	// remove one of the stream urls
	_, err = r.client.UpdateStream(ctx, egressID, &livekit.UpdateStreamRequest{
		EgressId:         egressID,
		RemoveOutputUrls: []string{streamUrl1},
	})
	require.NoError(t, err)

	time.Sleep(time.Second * 5)
	if r.Dotfiles {
		r.createDotFile(t, egressID)
	}

	// verify the remaining stream
	r.verifyStreams(t, p, streamUrl2)
	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		redactedUrl1:    livekit.StreamInfo_FINISHED,
		redactedUrl2:    livekit.StreamInfo_ACTIVE,
		redactedBadUrl1: livekit.StreamInfo_FAILED,
		redactedBadUrl2: livekit.StreamInfo_FAILED,
	})

	// stop
	time.Sleep(time.Second * 5)
	res := r.stopEgress(t, egressID)

	// verify egress info
	require.Empty(t, res.Error)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// check stream info
	require.Len(t, res.StreamResults, 4)
	for _, info := range res.StreamResults {
		require.NotZero(t, info.StartedAt)
		require.NotZero(t, info.EndedAt)

		switch info.Url {
		case redactedUrl1:
			require.Equal(t, livekit.StreamInfo_FINISHED.String(), info.Status.String())
			require.Greater(t, float64(info.Duration)/1e9, 15.0)

		case redactedUrl2:
			require.Equal(t, livekit.StreamInfo_FINISHED.String(), info.Status.String())
			require.Greater(t, float64(info.Duration)/1e9, 10.0)

		case redactedBadUrl1, redactedBadUrl2:
			require.Equal(t, livekit.StreamInfo_FAILED.String(), info.Status.String())

		default:
			t.Fatal("invalid stream url in result")
		}
	}
}

func (r *Runner) verifyStreams(t *testing.T, p *config.PipelineConfig, urls ...string) {
	for _, url := range urls {
		verify(t, url, p, nil, types.EgressTypeStream, false, r.sourceFramerate, false)
	}
}
