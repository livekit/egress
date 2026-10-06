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
	"math/rand/v2"
	"strings"
	"time"

	"github.com/frostbyte73/core"
	"github.com/linkdata/deadlock"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
)

const (
	initialBackoff    = time.Millisecond * 500
	maxBackoff        = time.Minute * 1
	drainPollInterval = time.Millisecond * 100

	// createFailedTTL outlives an aborted handler, including one whose pipeline is slow to stop
	createFailedTTL = time.Hour

	// beyond maxPendingUpdates, an update replaces the last unsent one when both have the same status
	maxPendingUpdates = 100
)

// UpdateEgress failure outcomes (livekit_egress_io_update_failures_total)
const (
	ioUpdateRetried   = "retried"
	ioUpdateAbandoned = "abandoned"
	ioUpdateFailed    = "failed"
	ioUpdateUnowned   = "unowned"
	ioUpdateCoalesced = "coalesced"
)

type SessionReporter interface {
	CreateEgress(ctx context.Context, info *livekit.EgressInfo) chan error
	UpdateEgress(ctx context.Context, info *livekit.EgressInfo) error

	// SessionStarted reports that an egress is running: its handler process
	// came up and reported itself ready. An implementation that meters an
	// egress can measure from here rather than from EgressInfo.StartedAt,
	// which is stamped while the request is parsed and so says nothing about
	// whether the egress ever ran.
	SessionStarted(ctx context.Context, egressID string)

	// SessionEnded reports that an egress is over and that nothing will send
	// another update for it, whatever its last update said. It is called once
	// the handler process has exited, however it died, so an implementation
	// holding per-egress state can release that state even when the egress'
	// own terminal update never arrived. An implementation that releases on
	// that update instead will be called for an egress it no longer holds, so
	// this has to tolerate being called more than once.
	SessionEnded(ctx context.Context, egressID string)
	UpdateMetrics(ctx context.Context, req *rpc.UpdateMetricsRequest) error
	Drain()
}

type sessionReporter struct {
	rpc.IOInfoClient

	createTimeout       time.Duration
	updateTimeout       time.Duration
	updateRetryDeadline time.Duration

	workers []*worker

	// log dedup only -- io health does not gate routability
	ioFailing atomic.Bool

	ioUpdateFailures *prometheus.CounterVec

	draining core.Fuse
	done     core.Fuse
}

type worker struct {
	mu deadlock.Mutex
	// creating buffers updates that arrive while CreateEgress is in flight
	creating map[string]*egressUpdates
	// pending holds an entry for every egress that is queued, in flight or waiting out a retry backoff
	pending map[string]*egressUpdates
	// createFailed holds egresses whose CreateEgress failed; their updates are dropped until a new
	// CreateEgress or createFailedTTL (kept past SessionEnded, a late HandlerFinished can still report)
	createFailed map[string]time.Time
	queue        chan string
}

// egressUpdates holds one egress' unsent updates, oldest first; below maxPendingUpdates every update is sent,
// above it a same-status update replaces the last unsent one.
type egressUpdates struct {
	updates []*update
	backoff time.Duration
}

// add appends u, or replaces the last update when over maxPendingUpdates and the status matches;
// it reports whether it replaced one. The first update is never replaced, since it may be in flight.
func (e *egressUpdates) add(u *update) bool {
	if n := len(e.updates); n >= maxPendingUpdates && e.updates[n-1].info.Status == u.info.Status {
		e.updates[n-1] = u
		return true
	}
	e.updates = append(e.updates, u)
	return false
}

type update struct {
	ctx      context.Context
	info     *livekit.EgressInfo
	deadline time.Time
}

func NewSessionReporter(conf *config.BaseConfig, bus psrpc.MessageBus) (SessionReporter, error) {
	client, err := rpc.NewIOInfoClient(bus, psrpc.WithClientSelectTimeout(conf.IOSelectionTimeout), rpc.WithClientObservability(logger.GetLogger()))
	if err != nil {
		return nil, err
	}

	ioUpdateFailures := newIOUpdateFailures(conf)
	prometheus.MustRegister(ioUpdateFailures)

	return newSessionReporter(conf, client, ioUpdateFailures), nil
}

func newSessionReporter(conf *config.BaseConfig, client rpc.IOInfoClient, ioUpdateFailures *prometheus.CounterVec) *sessionReporter {
	c := &sessionReporter{
		IOInfoClient:        client,
		createTimeout:       conf.IOCreateTimeout,
		updateTimeout:       conf.IOUpdateTimeout,
		updateRetryDeadline: conf.IOUpdateRetryDeadline,
		workers:             make([]*worker, conf.IOWorkers),
		ioUpdateFailures:    ioUpdateFailures,
	}

	for i := 0; i < conf.IOWorkers; i++ {
		c.workers[i] = &worker{
			creating:     make(map[string]*egressUpdates),
			pending:      make(map[string]*egressUpdates),
			createFailed: make(map[string]time.Time),
			queue:        make(chan string, 500),
		}
		go c.runWorker(c.workers[i])
	}

	return c
}

func newIOUpdateFailures(conf *config.BaseConfig) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "io_update_failures_total",
		Help:        "Total number of UpdateEgress failures and undelivered updates, by outcome",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "cluster_id": conf.ClusterID},
	}, []string{"outcome"})
}

func (c *sessionReporter) CreateEgress(ctx context.Context, info *livekit.EgressInfo) chan error {
	e := &egressUpdates{}
	w := c.getWorker(info.EgressId)

	w.mu.Lock()
	w.creating[info.EgressId] = e
	w.sweepCreateFailedLocked(info.EgressId)
	w.mu.Unlock()

	errChan := make(chan error, 1)
	go func() {
		_, err := c.IOInfoClient.CreateEgress(ctx, info, psrpc.WithRequestTimeout(c.createTimeout))

		w.mu.Lock()
		delete(w.creating, info.EgressId)
		if err == nil && len(e.updates) > 0 {
			err = c.enqueueLocked(w, info.EgressId, e.updates...)
		}
		if err != nil {
			logger.Errorw("failed to create egress", err, "egressID", info.EgressId)
			// any error fails the start and aborts the handler, so mark before errChan is sent
			w.markCreateFailedLocked(info.EgressId)
			c.ioUpdateFailures.WithLabelValues(ioUpdateUnowned).Add(float64(len(e.updates)))
		}
		w.mu.Unlock()

		errChan <- err
	}()

	return errChan
}

func (c *sessionReporter) UpdateEgress(ctx context.Context, info *livekit.EgressInfo) error {
	u := &update{
		ctx:  context.WithoutCancel(ctx),
		info: info,
	}
	if c.updateRetryDeadline > 0 {
		u.deadline = time.Now().Add(c.updateRetryDeadline)
	}

	w := c.getWorker(info.EgressId)

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, ok := w.createFailed[info.EgressId]; ok {
		c.ioUpdateFailures.WithLabelValues(ioUpdateUnowned).Inc()
		logger.Debugw("discarding update for egress whose create failed", "egressID", info.EgressId, "status", info.Status.String())
		return nil
	}
	if e := w.creating[info.EgressId]; e != nil {
		if e.add(u) {
			c.ioUpdateFailures.WithLabelValues(ioUpdateCoalesced).Inc()
		}
		return nil
	}

	return c.enqueueLocked(w, info.EgressId, u)
}

// Unsent updates are released as they are delivered or given up, and create-failure marks outlive
// SessionEnded by design, so there is nothing to do here.
func (c *sessionReporter) SessionStarted(_ context.Context, _ string) {}
func (c *sessionReporter) SessionEnded(_ context.Context, _ string)   {}

func (c *sessionReporter) UpdateMetrics(_ context.Context, _ *rpc.UpdateMetricsRequest) error {
	return nil
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
			for !w.idle() {
				select {
				case egressID := <-w.queue:
					c.handleUpdate(w, egressID)
				case <-time.After(drainPollInterval):
				}
			}
			c.done.Break()
			return
		}
	}
}

func (w *worker) idle() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pending) == 0
}

func (c *sessionReporter) getWorker(egressID string) *worker {
	h := fnv.New32a()
	_, _ = h.Write([]byte(egressID))
	return c.workers[int(h.Sum32())%len(c.workers)]
}

func (w *worker) markCreateFailedLocked(egressID string) {
	w.createFailed[egressID] = time.Now()
}

// sweepCreateFailedLocked clears the mark of an egress being created again and marks older than createFailedTTL.
func (w *worker) sweepCreateFailedLocked(egressID string) {
	delete(w.createFailed, egressID)
	now := time.Now()
	for id, at := range w.createFailed {
		if now.Sub(at) > createFailedTTL {
			delete(w.createFailed, id)
		}
	}
}

// enqueueLocked adds updates to the egress' pending entry, scheduling it if it had none.
func (c *sessionReporter) enqueueLocked(w *worker, egressID string, updates ...*update) error {
	if e := w.pending[egressID]; e != nil {
		for _, u := range updates {
			if e.add(u) {
				c.ioUpdateFailures.WithLabelValues(ioUpdateCoalesced).Inc()
			}
		}
		return nil
	}
	w.pending[egressID] = &egressUpdates{updates: updates}
	return w.scheduleLocked(egressID)
}

// scheduleLocked queues an egress whose entry is in pending; on a full queue the entry and its updates are dropped.
func (w *worker) scheduleLocked(egressID string) error {
	select {
	case w.queue <- egressID:
		return nil
	default:
		delete(w.pending, egressID)
		return errors.New("queue is full")
	}
}

// handleUpdate sends the egress' oldest unsent update; the next one is only sent after it succeeds or is given up.
func (c *sessionReporter) handleUpdate(w *worker, egressID string) {
	w.mu.Lock()
	e := w.pending[egressID]
	var u *update
	if e != nil && len(e.updates) > 0 {
		u = e.updates[0]
	}
	w.mu.Unlock()
	if u == nil {
		return
	}

	_, err := c.IOInfoClient.UpdateEgress(u.ctx, u.info, psrpc.WithRequestTimeout(c.updateTimeout))
	switch {
	case err == nil:
		if c.ioFailing.Swap(false) {
			logger.Infow("egress updates succeeding again", "egressID", egressID)
		}
		requestType, outputType := egress.GetTypes(u.info.Request)
		logger.Infow(strings.ToLower(u.info.Status.String()),
			"egressID", egressID,
			"requestType", requestType,
			"outputType", outputType,
			"error", u.info.Error,
			"code", u.info.ErrorCode,
			"details", u.info.Details,
		)

	case isRetryableError(err):
		if c.retryLater(w, egressID, e, u, err) {
			return
		}

	default:
		c.ioUpdateFailures.WithLabelValues(ioUpdateFailed).Inc()
		logger.Errorw("failed to update egress", err, "egressID", egressID)
	}

	c.advance(w, egressID, e)
}

// advance removes the oldest update and queues the egress again if more are waiting.
func (c *sessionReporter) advance(w *worker, egressID string, e *egressUpdates) {
	w.mu.Lock()
	defer w.mu.Unlock()

	e.updates[0] = nil
	e.updates = e.updates[1:]
	e.backoff = 0
	if len(e.updates) == 0 {
		delete(w.pending, egressID)
		return
	}

	c.scheduleOrDropLocked(w, egressID, e)
}

// retryLater schedules the egress to be queued again after a backoff, so the worker moves on to
// other egresses instead of blocking on this one. It returns false when the update is given up.
func (c *sessionReporter) retryLater(w *worker, egressID string, e *egressUpdates, u *update, err error) bool {
	c.ioUpdateFailures.WithLabelValues(ioUpdateRetried).Inc()
	if !c.ioFailing.Swap(true) {
		logger.Warnw("egress update failed, retrying", err, "egressID", egressID)
	}
	logger.Debugw("psrpc IO request failed", "error", err, "egressID", egressID)

	w.mu.Lock()
	defer w.mu.Unlock()

	if e.backoff == 0 {
		e.backoff = initialBackoff
	} else {
		e.backoff = min(e.backoff*2, maxBackoff)
	}
	delay := jitter(e.backoff)

	if !u.deadline.IsZero() && time.Now().Add(delay).After(u.deadline) {
		c.ioUpdateFailures.WithLabelValues(ioUpdateAbandoned).Inc()
		logger.Errorw("dropping egress update after retry deadline", err,
			"egressID", egressID,
			"status", u.info.Status.String(),
			"retryDeadline", c.updateRetryDeadline,
		)
		return false
	}

	time.AfterFunc(delay, func() {
		c.requeue(w, egressID)
	})
	return true
}

func (c *sessionReporter) requeue(w *worker, egressID string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if e := w.pending[egressID]; e != nil {
		c.scheduleOrDropLocked(w, egressID, e)
	}
}

func (c *sessionReporter) scheduleOrDropLocked(w *worker, egressID string, e *egressUpdates) {
	dropped := len(e.updates)
	if err := w.scheduleLocked(egressID); err != nil {
		c.ioUpdateFailures.WithLabelValues(ioUpdateAbandoned).Add(float64(dropped))
		logger.Errorw("dropping egress updates", err, "egressID", egressID, "count", dropped)
	}
}

func jitter(d time.Duration) time.Duration {
	return d/2 + rand.N(d/2)
}

func isRetryableError(err error) bool {
	var e psrpc.Error
	if errors.As(err, &e) {
		switch e.Code() {
		case psrpc.DeadlineExceeded, psrpc.Unavailable:
			return true
		}
	}
	return false
}
