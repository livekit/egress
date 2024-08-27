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
	"sync"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

type IOClient struct {
	rpc.IOInfoClient

	mu      sync.Mutex
	updates chan *livekit.EgressInfo
}

func NewIOClient(bus psrpc.MessageBus) (rpc.IOInfoClient, error) {
	client, err := rpc.NewIOInfoClient(bus)
	if err != nil {
		return nil, err
	}
	return &IOClient{
		IOInfoClient: client,
		updates:      make(chan *livekit.EgressInfo, 10),
	}, nil
}

func (c *IOClient) CreateEgress(ctx context.Context, info *livekit.EgressInfo, opts ...psrpc.RequestOption) (*emptypb.Empty, error) {
	_, err := c.IOInfoClient.CreateEgress(ctx, info, opts...)
	if err != nil {
		logger.Errorw("failed to create egress", err)
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (c *IOClient) UpdateEgress(ctx context.Context, info *livekit.EgressInfo, opts ...psrpc.RequestOption) (*emptypb.Empty, error) {
	c.updates <- info

	// ensure updates are sent sequentially
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		select {
		case update := <-c.updates:
			_, err := c.IOInfoClient.UpdateEgress(ctx, update, opts...)
			if err != nil {
				logger.Errorw("failed to update egress", err)
				return nil, err
			}

			requestType, outputType := egress.GetTypes(update.Request)
			switch update.Status {
			case livekit.EgressStatus_EGRESS_FAILED:
				logger.Warnw("egress failed", errors.New(update.Error),
					"egressID", update.EgressId,
					"requestType", requestType,
					"outputType", outputType,
				)
			case livekit.EgressStatus_EGRESS_COMPLETE:
				logger.Infow("egress completed",
					"egressID", update.EgressId,
					"requestType", requestType,
					"outputType", outputType,
				)
			default:
				logger.Infow("egress updated",
					"egressID", update.EgressId,
					"requestType", requestType,
					"outputType", outputType,
					"status", update.Status,
				)
			}
		default:
			return &emptypb.Empty{}, nil
		}
	}
}

func (c *IOClient) UpdateMetrics(ctx context.Context, req *rpc.UpdateMetricsRequest, opts ...psrpc.RequestOption) (*emptypb.Empty, error) {
	_, err := c.IOInfoClient.UpdateMetrics(ctx, req, opts...)
	if err != nil {
		logger.Errorw("failed to update ms", err)
		return nil, err
	}
	return &emptypb.Empty{}, nil
}
