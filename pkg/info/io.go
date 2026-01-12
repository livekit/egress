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
	"math"
	"strings"
	"time"

	"github.com/frostbyte73/core"
	"github.com/linkdata/deadlock"

	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
)

const (
	numWorkers                     = 5
	maxBackoff                     = time.Minute * 1
	unhealthyShutdownWatchdogDelay = 20 * time.Second // TODO change to 10 min once we undrerstant PSRPC failures
)

type SessionReporter interface {
	CreateEgress(ctx context.Context, info *livekit.EgressInfo) chan error
	UpdateEgress(ctx context.Context, info *livekit.EgressInfo) error
	UpdateMetrics(ctx context.Context, req *rpc.UpdateMetricsRequest) error
	IsHealthy() bool
	SetWatchdogHandler(w func())
	Drain()
}

type sessionReporter struct {
	rpc.IOInfoClient

	createTimeout time.Duration
	updateTimeout time.Duration

	workers []*worker

	healthyLock            deadlock.Mutex
	healthy                bool
	healthyWatchdogHandler func()
	healthyTimer           *time.Timer

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

func NewSessionReporter(conf *config.BaseConfig, bus psrpc.MessageBus) (SessionReporter, error) {
	client, err := rpc.NewIOInfoClient(bus, psrpc.WithClientSelectTimeout(conf.IOSelectionTimeout), rpc.WithClientObservability(logger.GetLogger()))
	if err != nil {
		return nil, err
	}

	c := &sessionReporter{
		IOInfoClient:  client,
		createTimeout: conf.IOCreateTimeout,
		updateTimeout: conf.IOUpdateTimeout,
		workers:       make([]*worker, conf.IOWorkers),
	}
	c.healthy = true
	c.healthyTimer = time.AfterFunc(time.Duration(math.MaxInt64), func() {
		c.healthyLock.Lock()
		defer c.healthyLock.Unlock()

		logger.Errorw("io client watchdog triggered", errors.New("io client unhealthy"))
		if c.healthyWatchdogHandler != nil {
			c.healthyWatchdogHandler()
		}
		// Do not wait for the event queue to drain
		c.done.Break()
	})

	for i := 0; i < conf.IOWorkers; i++ {
		c.workers[i] = &worker{
			creating: make(map[string]*update),
			updates:  make(map[string]*update),
			queue:    make(chan string, 500),
		}
		go c.runWorker(c.workers[i])
	}

	return c, nil
}

func (c *sessionReporter) CreateEgress(ctx context.Context, info *livekit.EgressInfo) chan error {
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

func (c *sessionReporter) UpdateEgress(ctx context.Context, info *livekit.EgressInfo) error {
	ctx = context.WithoutCancel(ctx)

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

func (c *sessionReporter) UpdateMetrics(_ context.Context, _ *rpc.UpdateMetricsRequest) error {
	return nil
}

func (c *sessionReporter) SetWatchdogHandler(w func()) {
	c.healthyLock.Lock()
	defer c.healthyLock.Unlock()

	c.healthyWatchdogHandler = w
}

func (c *sessionReporter) IsHealthy() bool {
	c.healthyLock.Lock()
	defer c.healthyLock.Unlock()

	return c.healthy
}

func (c *sessionReporter) Drain() {
	c.draining.Break()
	<-c.done.Watch()
}

func (c *sessionReporter) runWorker(w *worker) {
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

func (c *sessionReporter) getWorker(egressID string) *worker {
	h := fnv.New32a()
	_, _ = h.Write([]byte(egressID))
	return c.workers[int(h.Sum32())%len(c.workers)]
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

func (c *sessionReporter) handleUpdate(w *worker, egressID string) {
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
			if isRetryableError(err) {
				if c.setHealthy(false) {
					logger.Warnw("io connection unhealthy", err, "egressID", u.info.EgressId)
				}
				logger.Debugw("psrpc IO request failed", "error", err, "egressID", u.info.EgressId)

				d = min(d*2, maxBackoff)
				time.Sleep(d)

				select {
				case <-u.ctx.Done():
					logger.Infow("failed to update egress on expired context", "egressID", u.info.EgressId)
					return
				default:
					continue
				}
			}

			logger.Errorw("failed to update egress", err, "egressID", u.info.EgressId)
			return
		}

		if !c.setHealthy(true) {
			logger.Infow("io connection restored", "egressID", u.info.EgressId)
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

func (c *sessionReporter) setHealthy(isHealthy bool) bool {
	c.healthyLock.Lock()
	defer c.healthyLock.Unlock()

	oldHealthy := c.healthy

	switch c.healthy {
	case true:
		if !isHealthy {
			c.healthyTimer.Reset(unhealthyShutdownWatchdogDelay)
		}
	case false:
		if isHealthy {
			c.healthyTimer.Reset(time.Duration(math.MaxInt64))
		}
	}

	c.healthy = isHealthy

	return oldHealthy
}

func isRetryableError(err error) bool {
	return errors.Is(err, psrpc.ErrRequestTimedOut) || errors.Is(err, psrpc.ErrNoResponse)
}
