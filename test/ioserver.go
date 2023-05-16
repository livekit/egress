//go:build integration

package test

import (
	"context"

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
