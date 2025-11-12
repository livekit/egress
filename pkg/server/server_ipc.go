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
	"net/http"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

func (s *Server) HandlerReady(_ context.Context, req *ipc.HandlerReadyRequest) (*emptypb.Empty, error) {
	logger.Debugw("handler ready", "egressID", req.EgressId)
	if err := s.HandlerStarted(req.EgressId); err != nil {
		return nil, err
	}

	logger.Debugw("handler ready completed", "egressID", req.EgressId)
	return &emptypb.Empty{}, nil
}

func (s *Server) HandlerUpdate(_ context.Context, info *livekit.EgressInfo) (*emptypb.Empty, error) {
	logger.Debugw("handler update", "egressID", info.EgressId)
	if err := s.ioClient.UpdateEgress(context.Background(), info); err != nil {
		logger.Errorw("failed to update egress", err, "egressID", info.EgressId)
	}

	if info.ErrorCode == int32(http.StatusInternalServerError) {
		logger.Errorw("internal error, shutting down", errors.New(info.Error))
		s.Shutdown(false, false)
	}
	logger.Debugw("handler update completed", "egressID", info.EgressId)

	return &emptypb.Empty{}, nil
}

func (s *Server) HandlerFinished(_ context.Context, req *ipc.HandlerFinishedRequest) (*emptypb.Empty, error) {
	logger.Debugw("handler finished", "egressID", req.EgressId)
	if err := s.ioClient.UpdateEgress(context.Background(), req.Info); err != nil {
		logger.Errorw("failed to update egress", err, "egressID", req.EgressId)
	}

	if err := s.StoreProcessEndedMetrics(req.EgressId, req.Metrics); err != nil {
		logger.Errorw("failed to store metrics", err, "egressID", req.EgressId)
	}

	logger.Debugw("handler finished completed", "egressID", req.EgressId)
	return &emptypb.Empty{}, nil
}
