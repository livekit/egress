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

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
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
	server, err := rpc.NewIOInfoServer(s, bus)
	if err != nil {
		return nil, err
	}
	s.server = server
	return s, nil
}

func (s *ioTestServer) CreateEgress(_ context.Context, _ *livekit.EgressInfo) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *ioTestServer) UpdateEgress(_ context.Context, info *livekit.EgressInfo) (*emptypb.Empty, error) {
	s.updates <- info
	return &emptypb.Empty{}, nil
}

func (s *ioTestServer) UpdateMetrics(_ context.Context, req *rpc.UpdateMetricsRequest) (*emptypb.Empty, error) {
	logger.Infow("egress metrics", "egressID", req.Info.EgressId, "avgCPU", req.AvgCpuUsage, "maxCPU", req.MaxCpuUsage)
	return &emptypb.Empty{}, nil
}
