//go:build integration

package test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
)

func (r *Runner) runMultipleTest(
	t *testing.T,
	req *rpc.StartEgressRequest,
	file, stream, segments bool,
	filenameSuffix livekit.SegmentedFileSuffix,
) {
	egressID := r.startEgress(t, req)
	time.Sleep(time.Second * 10)

	// get params
	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	if stream {
		_, err := r.client.UpdateStream(context.Background(), egressID, &livekit.UpdateStreamRequest{
			EgressId:      egressID,
			AddOutputUrls: []string{streamUrl1},
		})
		require.NoError(t, err)

		time.Sleep(time.Second * 10)
		r.verifyStreams(t, p, streamUrl1)
		r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
			redactedUrl1: livekit.StreamInfo_ACTIVE,
		})
		time.Sleep(time.Second * 10)
	} else {
		time.Sleep(time.Second * 20)
	}

	res := r.stopEgress(t, egressID)
	if file {
		r.verifyFile(t, p, res)
	}
	if segments {
		r.verifySegments(t, p, filenameSuffix, res)
	}
}
