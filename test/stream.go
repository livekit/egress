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

	time.Sleep(time.Second * 5)

	// get params
	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	// verify stream
	require.Equal(t, test.expectVideoTranscoding, p.VideoTranscoding)
	r.verifyStreams(t, p, streamUrl1)

	// add one good stream url and a couple bad ones
	_, err = r.client.UpdateStream(ctx, egressID, &livekit.UpdateStreamRequest{
		EgressId:      egressID,
		AddOutputUrls: []string{badStreamUrl2, streamUrl2},
	})
	require.NoError(t, err)

	time.Sleep(time.Second * 5)

	update := r.getUpdate(t, egressID)
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE.String(), update.Status.String())
	require.Len(t, update.StreamResults, 4)
	for _, info := range update.StreamResults {
		switch info.Url {
		case redactedUrl1, redactedUrl2:
			require.Equal(t, livekit.StreamInfo_ACTIVE.String(), info.Status.String())

		case redactedBadUrl1, redactedBadUrl2:
			require.Equal(t, livekit.StreamInfo_FAILED.String(), info.Status.String())

		default:
			t.Fatal("invalid stream url in result")
		}
	}

	require.Equal(t, test.expectVideoTranscoding, p.VideoTranscoding)

	// verify the good stream urls
	r.verifyStreams(t, p, streamUrl1, streamUrl2)

	// remove one of the stream urls
	_, err = r.client.UpdateStream(ctx, egressID, &livekit.UpdateStreamRequest{
		EgressId:         egressID,
		RemoveOutputUrls: []string{streamUrl1},
	})
	require.NoError(t, err)

	time.Sleep(time.Second * 5)

	// verify the remaining stream
	r.verifyStreams(t, p, streamUrl2)

	time.Sleep(time.Second * 10)

	// stop
	res := r.stopEgress(t, egressID)

	// verify egress info
	require.Empty(t, res.Error)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// check stream info
	require.Len(t, res.StreamResults, 3)
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

		case redactedBadUrl1:
			require.Equal(t, livekit.StreamInfo_FAILED.String(), info.Status.String())

		case redactedBadUrl2:
			require.Equal(t, livekit.StreamInfo_FAILED.String(), info.Status.String())

		default:
			t.Fatal("invalid stream url in result")
		}
	}
}

func (r *Runner) verifyStreams(t *testing.T, p *config.PipelineConfig, urls ...string) {
	for _, url := range urls {
		verify(t, url, p, nil, types.EgressTypeStream, false, r.sourceFramerate)
	}
}
