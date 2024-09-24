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
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

const (
	ioTimeout  = time.Second * 30
	maxBackoff = time.Minute * 10
)

type IOClient interface {
	CreateEgress(ctx context.Context, info *livekit.EgressInfo) chan error
	UpdateEgress(ctx context.Context, info *livekit.EgressInfo) error
	UpdateMetrics(ctx context.Context, req *rpc.UpdateMetricsRequest) error
	IsHealthy() bool
	Drain()
}

type ioClient struct {
	rpc.IOInfoClient

	mu       sync.Mutex
	egresses map[string]*egressCreation
	updates  chan *update

	healthy  atomic.Bool
	draining core.Fuse
	done     core.Fuse
}

type egressCreation struct {
	pending *update
}

type update struct {
	ctx  context.Context
	info *livekit.EgressInfo
}

func NewIOClient(bus psrpc.MessageBus) (IOClient, error) {
	client, err := rpc.NewIOInfoClient(bus, psrpc.WithClientTimeout(ioTimeout))
	if err != nil {
		return nil, err
	}

	c := &ioClient{
		IOInfoClient: client,
		egresses:     make(map[string]*egressCreation),
		updates:      make(chan *update, 1000),
	}
	c.healthy.Store(true)
	go c.updateWorker()

	return c, nil
}

func (c *ioClient) CreateEgress(ctx context.Context, info *livekit.EgressInfo) chan error {
	e := &egressCreation{}

	c.mu.Lock()
	c.egresses[info.EgressId] = e
	c.mu.Unlock()

	errChan := make(chan error, 1)
	go func() {
		_, err := c.IOInfoClient.CreateEgress(ctx, info)

		c.mu.Lock()
		defer c.mu.Unlock()

		delete(c.egresses, info.EgressId)

		if err != nil {
			logger.Errorw("failed to create egress", err)
			if errors.Is(err, psrpc.ErrRequestTimedOut) && c.healthy.Swap(false) {
				logger.Infow("io connection unhealthy")
			}
			errChan <- err
			return
		} else if !c.healthy.Swap(true) {
			logger.Infow("io connection restored")
		}

		if e.pending != nil {
			c.updates <- e.pending
		}
		errChan <- nil
	}()

	return errChan
}

func (c *ioClient) UpdateEgress(ctx context.Context, info *livekit.EgressInfo) error {
	u := &update{
		ctx:  ctx,
		info: info,
	}

	c.mu.Lock()
	if e, ok := c.egresses[info.EgressId]; ok {
		e.pending = u
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	c.updates <- u
	return nil
}

func (c *ioClient) updateWorker() {
	draining := c.draining.Watch()
	for {
		select {
		case u := <-c.updates:
			c.sendUpdate(u)
		case <-draining:
			c.done.Break()
			return
		}
	}
}

func (c *ioClient) sendUpdate(u *update) {
	d := time.Millisecond * 250
	for {
		if _, err := c.IOInfoClient.UpdateEgress(u.ctx, u.info); err != nil {
			if errors.Is(err, psrpc.ErrRequestTimedOut) {
				if c.healthy.Swap(false) {
					logger.Infow("io connection unhealthy")
				}
				d = min(d*2, maxBackoff)
				time.Sleep(d)
				continue
			}

			logger.Errorw("failed to update egress", err)
			return
		}

		if !c.healthy.Swap(true) {
			logger.Infow("io connection restored")
		}
		requestType, outputType := egress.GetTypes(u.info.Request)
		logger.Infow(strings.ToLower(u.info.Status.String()),
			"requestType", requestType,
			"outputType", outputType,
			"error", u.info.Error,
			"code", u.info.ErrorCode,
			"details", u.info.Details,
		)
		return
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

func (c *ioClient) IsHealthy() bool {
	return c.healthy.Load()
}

func (c *ioClient) Drain() {
	c.draining.Break()
	<-c.done.Watch()
}
