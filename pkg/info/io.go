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
	"hash/fnv"
	"strings"
	"time"

	"github.com/frostbyte73/core"
	"github.com/linkdata/deadlock"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

const (
	numWorkers = 5
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

	createTimeout time.Duration
	updateTimeout time.Duration

	workers []*worker

	healthy  atomic.Bool
	draining core.Fuse
	done     core.Fuse
}

type worker struct {
	mu       deadlock.Mutex
	creating map[string]*update
	updates  map[string]*update
	queue    chan string
}

type update struct {
	ctx  context.Context
	info *livekit.EgressInfo
}

func NewIOClient(conf *config.BaseConfig, bus psrpc.MessageBus) (IOClient, error) {
	client, err := rpc.NewIOInfoClient(bus)
	if err != nil {
		return nil, err
	}

	c := &ioClient{
		IOInfoClient:  client,
		createTimeout: conf.IOCreateTimeout,
		updateTimeout: conf.IOUpdateTimeout,
		workers:       make([]*worker, numWorkers),
	}
	c.healthy.Store(true)

	for i := 0; i < numWorkers; i++ {
		c.workers[i] = &worker{
			creating: make(map[string]*update),
			updates:  make(map[string]*update),
			queue:    make(chan string, 500),
		}
		go c.runWorker(c.workers[i])
	}

	return c, nil
}

func (c *ioClient) CreateEgress(ctx context.Context, info *livekit.EgressInfo) chan error {
	u := &update{}
	w := c.getWorker(info.EgressId)

	w.mu.Lock()
	w.creating[info.EgressId] = u
	w.mu.Unlock()

	errChan := make(chan error, 1)
	go func() {
		_, err := c.IOInfoClient.CreateEgress(ctx, info, psrpc.WithRequestTimeout(c.createTimeout))

		w.mu.Lock()
		delete(w.creating, info.EgressId)
		if err != nil {
			logger.Errorw("failed to create egress", err, "egressID", info.EgressId)
			delete(w.updates, info.EgressId)
		} else if u.info != nil {
			err = w.submit(u)
		}
		w.mu.Unlock()

		errChan <- err
	}()

	return errChan
}

func (c *ioClient) UpdateEgress(ctx context.Context, info *livekit.EgressInfo) error {
	w := c.getWorker(info.EgressId)

	w.mu.Lock()
	defer w.mu.Unlock()

	u := w.creating[info.EgressId]
	if u == nil {
		u = w.updates[info.EgressId]
	}
	if u != nil {
		u.ctx = ctx
		u.info = info
		return nil
	}

	return w.submit(&update{
		ctx:  ctx,
		info: info,
	})
}

func (c *ioClient) UpdateMetrics(ctx context.Context, req *rpc.UpdateMetricsRequest) error {
	return nil
}

func (c *ioClient) IsHealthy() bool {
	return c.healthy.Load()
}

func (c *ioClient) Drain() {
	c.draining.Break()
	<-c.done.Watch()
}

func (c *ioClient) runWorker(w *worker) {
	draining := c.draining.Watch()
	for {
		select {
		case egressID := <-w.queue:
			c.handleUpdate(w, egressID)
		case <-draining:
			for {
				select {
				case egressID := <-w.queue:
					c.handleUpdate(w, egressID)
				default:
					c.done.Break()
					return
				}
			}
		}
	}
}

func (c *ioClient) getWorker(egressID string) *worker {
	h := fnv.New32a()
	_, _ = h.Write([]byte(egressID))
	return c.workers[int(h.Sum32())%numWorkers]
}

func (w *worker) submit(u *update) error {
	w.updates[u.info.EgressId] = u

	select {
	case w.queue <- u.info.EgressId:
		return nil
	default:
		delete(w.updates, u.info.EgressId)
		return errors.New("queue is full")
	}
}

func (c *ioClient) handleUpdate(w *worker, egressID string) {
	w.mu.Lock()
	u := w.updates[egressID]
	delete(w.updates, egressID)
	w.mu.Unlock()
	if u == nil {
		return
	}

	d := time.Millisecond * 250
	for {
		if _, err := c.IOInfoClient.UpdateEgress(u.ctx, u.info, psrpc.WithRequestTimeout(c.updateTimeout)); err != nil {
			if errors.Is(err, psrpc.ErrRequestTimedOut) {
				if c.healthy.Swap(false) {
					logger.Warnw("io connection unhealthy", err)
				}
				d = min(d*2, maxBackoff)
				time.Sleep(d)
				continue
			}

			logger.Errorw("failed to update egress", err, "egressID", u.info.EgressId)
			return
		}

		if !c.healthy.Swap(true) {
			logger.Infow("io connection restored")
		}
		requestType, outputType := egress.GetTypes(u.info.Request)
		logger.Infow(strings.ToLower(u.info.Status.String()),
			"egressID", u.info.EgressId,
			"requestType", requestType,
			"outputType", outputType,
			"error", u.info.Error,
			"code", u.info.ErrorCode,
			"details", u.info.Details,
		)
		return
	}
}
