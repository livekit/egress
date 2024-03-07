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

package service

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
)

func (s *Service) HandlerReady(_ context.Context, req *ipc.HandlerReadyRequest) (*emptypb.Empty, error) {
	s.mu.RLock()
	p, ok := s.activeHandlers[req.EgressId]
	s.mu.RUnlock()

	if !ok {
		return nil, errors.ErrEgressNotFound
	}

	close(p.ready)
	return &emptypb.Empty{}, nil
}

func (s *Service) HandlerShuttingDown(_ context.Context, req *ipc.HandlerShuttingDownRequest) (*emptypb.Empty, error) {
	err := s.storeProcessEndedMetrics(req.EgressId, req.Metrics)
	if err != nil {
		return nil, err
	}

	return &emptypb.Empty{}, nil
}
