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

package server

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

func (s *Server) HandlerReady(_ context.Context, req *ipc.HandlerReadyRequest) (*emptypb.Empty, error) {
	if err := s.HandlerStarted(req.EgressId); err != nil {
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

func (s *Server) HandlerUpdate(ctx context.Context, info *livekit.EgressInfo) (*emptypb.Empty, error) {
	if _, err := s.ioClient.UpdateEgress(ctx, info); err != nil {
		logger.Errorw("failed to update egress", err)
	}

	return &emptypb.Empty{}, nil
}

func (s *Server) HandlerFinished(ctx context.Context, req *ipc.HandlerFinishedRequest) (*emptypb.Empty, error) {
	_, err := s.ioClient.UpdateEgress(ctx, req.Info)
	if err != nil {
		logger.Errorw("failed to update egress", err)
	}

	if err = s.StoreProcessEndedMetrics(req.EgressId, req.Metrics); err != nil {
		logger.Errorw("failed to store ms", err)
	}

	return &emptypb.Empty{}, nil
}
