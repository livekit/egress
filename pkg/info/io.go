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

package info

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/frostbyte73/core"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

const (
	ioTimeout = time.Second * 30
)

type IOClient interface {
	CreateEgress(ctx context.Context, info *livekit.EgressInfo) chan error
	UpdateEgress(ctx context.Context, info *livekit.EgressInfo) error
	UpdateMetrics(ctx context.Context, req *rpc.UpdateMetricsRequest) error
	Drain()
}

type ioClient struct {
	rpc.IOInfoClient

	mu       sync.Mutex
	egresses map[string]*egressIOClient
}

type egressIOClient struct {
	created core.Fuse
	aborted core.Fuse

	mu      sync.Mutex
	pending chan *livekit.EgressInfo
}

func NewIOClient(bus psrpc.MessageBus) (IOClient, error) {
	client, err := rpc.NewIOInfoClient(bus, psrpc.WithClientTimeout(ioTimeout))
	if err != nil {
		return nil, err
	}

	return &ioClient{
		IOInfoClient: client,
		egresses:     make(map[string]*egressIOClient),
	}, nil
}

func (c *ioClient) CreateEgress(ctx context.Context, info *livekit.EgressInfo) chan error {
	e := &egressIOClient{
		pending: make(chan *livekit.EgressInfo, 10),
	}
	c.mu.Lock()
	c.egresses[info.EgressId] = e
	c.mu.Unlock()

	errChan := make(chan error, 1)
	go func() {
		_, err := c.IOInfoClient.CreateEgress(ctx, info)
		if err != nil {
			logger.Errorw("failed to create egress", err)
			e.aborted.Break()
			errChan <- err

			c.mu.Lock()
			delete(c.egresses, info.EgressId)
			c.mu.Unlock()
		} else {
			e.created.Break()
			errChan <- nil
		}
	}()

	return errChan
}

func (c *ioClient) UpdateEgress(ctx context.Context, info *livekit.EgressInfo) error {
	c.mu.Lock()
	e, ok := c.egresses[info.EgressId]
	c.mu.Unlock()
	if !ok {
		return errors.ErrEgressNotFound
	}

	// ensure updates are sent sequentially
	e.pending <- info

	select {
	case <-e.created.Watch():
		// egress was created, continue
	case <-e.aborted.Watch():
		// egress was aborted, ignore
		return nil
	}

	// ensure only one thread is sending updates sequentially
	e.mu.Lock()
	defer e.mu.Unlock()
	for {
		select {
		case update := <-e.pending:
			var err error
			for i := 0; i < 10; i++ {
				_, err = c.IOInfoClient.UpdateEgress(ctx, update)
				if err == nil {
					break
				}
				time.Sleep(time.Millisecond * 100 * time.Duration(i))
			}
			if err != nil {
				logger.Warnw("failed to update egress", err, "egressID", update.EgressId)
				return err
			}

			requestType, outputType := egress.GetTypes(update.Request)
			logger.Infow(strings.ToLower(update.Status.String()),
				"requestType", requestType,
				"outputType", outputType,
				"error", update.Error,
				"code", update.ErrorCode,
				"details", update.Details,
			)

			switch update.Status {
			case livekit.EgressStatus_EGRESS_COMPLETE,
				livekit.EgressStatus_EGRESS_FAILED,
				livekit.EgressStatus_EGRESS_ABORTED,
				livekit.EgressStatus_EGRESS_LIMIT_REACHED:
				// egress is done, delete ioEgressClient
				c.mu.Lock()
				delete(c.egresses, info.EgressId)
				c.mu.Unlock()
			}

		default:
			return nil
		}
	}
}

func (c *ioClient) UpdateMetrics(ctx context.Context, req *rpc.UpdateMetricsRequest) error {
	_, err := c.IOInfoClient.UpdateMetrics(ctx, req)
	if err != nil {
		logger.Errorw("failed to update ms", err)
		return err
	}

	return nil
}

func (c *ioClient) Drain() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		c.mu.Lock()
		if len(c.egresses) == 0 {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
	}
}
