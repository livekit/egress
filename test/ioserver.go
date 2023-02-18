package test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

type ioTestServer struct {
	rpc.IOInfoServerImpl
	server  rpc.IOInfoServer
	updates chan *livekit.EgressInfo
}

func newIOTestServer(bus psrpc.MessageBus, updates chan *livekit.EgressInfo) (*ioTestServer, error) {
	s := &ioTestServer{
		updates: updates,
	}
	server, err := rpc.NewIOInfoServer("test_io", s, bus)
	if err != nil {
		return nil, err
	}
	s.server = server
	return s, nil
}

func (s *ioTestServer) UpdateEgressInfo(ctx context.Context, info *livekit.EgressInfo) (*emptypb.Empty, error) {
	s.updates <- info
	return &emptypb.Empty{}, nil
}

func checkUpdate(t *testing.T, conf *TestConfig, egressID string, status livekit.EgressStatus) *livekit.EgressInfo {
	info := getUpdate(t, conf, egressID)

	require.Equal(t, status.String(), info.Status.String())
	if info.Status == livekit.EgressStatus_EGRESS_FAILED {
		require.NotEmpty(t, info.Error, "failed egress missing error")
	} else {
		require.Empty(t, info.Error, "status %s with error %s", info.Status.String(), info.Error)
	}

	return info
}

func checkStoppedEgress(t *testing.T, conf *TestConfig, egressID string, expectedStatus livekit.EgressStatus) *livekit.EgressInfo {
	// check ending update
	checkUpdate(t, conf, egressID, livekit.EgressStatus_EGRESS_ENDING)

	// get final info
	info := checkUpdate(t, conf, egressID, expectedStatus)

	// check status
	if conf.HealthPort != 0 {
		status := getStatus(t, conf.svc)
		require.Len(t, status, 1)
	}

	return info
}

func getUpdate(t *testing.T, conf *TestConfig, egressID string) *livekit.EgressInfo {
	for {
		select {
		case info := <-conf.psrpcUpdates:
			if info.EgressId == egressID {
				return info
			}

		case <-time.After(time.Minute):
			t.Fatal("no update from results channel")
			return nil
		}
	}
}
