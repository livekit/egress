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
	"maps"
	"os"
	"os/exec"
	"path"
	"slices"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/frostbyte73/core"
	"github.com/linkdata/deadlock"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/egress/pkg/stats"
)

const (
	launchTimeout = 10 * time.Second
	// metricsGatherTimeout bounds the live-metrics IPC call
	metricsGatherTimeout = 2 * time.Second
)

//go:generate go tool github.com/maxbrunsfeld/counterfeiter/v6  . ProcessManager

type ProcessManager interface {
	Launch(ctx context.Context, handlerID string, req *rpc.StartEgressRequest, info *livekit.EgressInfo, cmd *exec.Cmd) error
	// SetHandlerTopicHooks installs callbacks used to advertise and remove the
	// per-egress handler RPC topics. Registration happens inside Launch, after
	// the handler's IPC client exists; deregistration happens in ProcessFinished.
	// The hooks are stored without synchronization and read by Launch and
	// ProcessFinished, so this must be called once during server construction,
	// before any handler can be launched.
	SetHandlerTopicHooks(register func(egressID string) error, deregister func(egressID string))
	GetContext(egressID string) context.Context
	AlreadyExists(egressID string) bool
	HandlerStarted(egressID string) error
	GetActiveEgressIDs() []string
	GetStatus(info map[string]interface{})
	GetGatherers() []prometheus.Gatherer
	GetGRPCClient(egressID string) (ipc.EgressHandlerClient, error)
	KillAll()
	AbortProcess(egressID string, err error)
	StopProcess(egressID string, reason string)
	KillProcess(egressID string, reason string, err error)
	SetExitReason(egressID string, reason string)
	GetKillReason(egressID string) string
	ProcessFinished(egressID string)
	// StoreAccumulatableMetrics caches the accumulatable portion of a handler's metrics
	StoreAccumulatableMetrics(egressID string, metrics []*dto.MetricFamily)
	// FinalizeMetrics suppresses a handler's live values (Process.Gather returns
	// empty afterwards) and returns its cached accumulatable tally.
	FinalizeMetrics(egressID string) (metrics []*dto.MetricFamily, alreadyFinalized bool)
}

type processManager struct {
	mu             deadlock.RWMutex
	activeHandlers map[string]*Process

	registerTopics   func(egressID string) error
	deregisterTopics func(egressID string)
}

func NewProcessManager() ProcessManager {
	return &processManager{
		activeHandlers: make(map[string]*Process),
	}
}

func (pm *processManager) SetHandlerTopicHooks(register func(egressID string) error, deregister func(egressID string)) {
	pm.registerTopics = register
	pm.deregisterTopics = deregister
}

func (pm *processManager) StoreAccumulatableMetrics(egressID string, metrics []*dto.MetricFamily) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if p, ok := pm.activeHandlers[egressID]; ok {
		p.storeAccumulatableMetrics(metrics)
	}
}

func (pm *processManager) FinalizeMetrics(egressID string) (metrics []*dto.MetricFamily, alreadyFinalized bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	p, ok := pm.activeHandlers[egressID]
	if !ok || p.metricsFinalized.Swap(true) {
		return nil, true
	}
	return p.getAccumulatableMetrics(), false
}

func (pm *processManager) Launch(
	ctx context.Context,
	handlerID string,
	req *rpc.StartEgressRequest,
	info *livekit.EgressInfo,
	cmd *exec.Cmd,
) error {
	ipcHandlerDir := path.Join(config.TmpDir, handlerID)
	if err := os.MkdirAll(ipcHandlerDir, 0755); err != nil {
		return err
	}

	ipcClient, err := ipc.NewHandlerClient(ipcHandlerDir)
	if err != nil {
		return err
	}

	p := &Process{
		ctx:              ctx,
		handlerID:        handlerID,
		req:              req,
		info:             info,
		cmd:              cmd,
		ipcHandlerClient: ipcClient,
		ready:            make(chan struct{}),
	}

	pm.mu.Lock()
	pm.activeHandlers[info.EgressId] = p
	pm.mu.Unlock()

	// advertise the handler RPC topics only once the IPC client can serve
	// them; requests arriving during handler startup wait on the connection.
	// The map entry is cleaned up by ProcessFinished on failure, same as the
	// cmd.Start error path.
	if pm.registerTopics != nil {
		if err = pm.registerTopics(info.EgressId); err != nil {
			logger.Errorw("could not register handler rpc topics", err, "egressID", info.EgressId)
			return err
		}
	}

	if err = cmd.Start(); err != nil {
		logger.Errorw("could not launch process", err, "egressID", info.EgressId)
		return err
	}

	select {
	case <-p.ready:
		return nil

	case <-time.After(launchTimeout):
		logger.Warnw("no response from handler", nil, "egressID", info.EgressId)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.ErrHandlerFailedToStart
	}
}

func (pm *processManager) GetContext(egressID string) context.Context {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if p, ok := pm.activeHandlers[egressID]; ok {
		return p.ctx
	}

	return context.Background()
}

func (pm *processManager) AlreadyExists(egressID string) bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	_, ok := pm.activeHandlers[egressID]
	return ok
}

func (pm *processManager) HandlerStarted(egressID string) error {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if p, ok := pm.activeHandlers[egressID]; ok {
		close(p.ready)
		return nil
	}

	return errors.ErrEgressNotFound
}

func (pm *processManager) GetActiveEgressIDs() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	egressIDs := make([]string, 0, len(pm.activeHandlers))
	for egressID := range pm.activeHandlers {
		egressIDs = append(egressIDs, egressID)
	}

	return egressIDs
}

func (pm *processManager) GetStatus(info map[string]interface{}) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for _, h := range pm.activeHandlers {
		info[h.req.EgressId] = h.req.Request
	}
}

func (pm *processManager) GetGatherers() []prometheus.Gatherer {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	handlers := make([]prometheus.Gatherer, 0, len(pm.activeHandlers))
	for _, p := range pm.activeHandlers {
		handlers = append(handlers, p)
	}

	return handlers
}

func (pm *processManager) GetGRPCClient(egressID string) (ipc.EgressHandlerClient, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	h, ok := pm.activeHandlers[egressID]
	if !ok {
		return nil, errors.ErrEgressNotFound
	}
	return h.ipcHandlerClient, nil
}

func (pm *processManager) KillAll() {
	pm.mu.Lock()
	handlers := slices.Collect(maps.Values(pm.activeHandlers))
	for _, h := range handlers {
		h.killReason = stats.ResultKilledShutdown
	}
	pm.mu.Unlock()

	for _, h := range handlers {
		h.kill(errors.ErrShuttingDown)
	}
}

func (pm *processManager) AbortProcess(egressID string, err error) {
	logger.Infow("aborting egress", err, "egressID", egressID)
	pm.mu.Lock()
	h, ok := pm.activeHandlers[egressID]
	if ok {
		h.killReason = stats.ResultAborted
	}
	pm.mu.Unlock()

	if ok {
		logger.Warnw("aborting handler", err, "egressID", egressID)
		h.kill(err)
		h.ipcHandlerClient.Close()
	}
	logger.Infow("aborting egress completed", "egressID", egressID)
}

// endReasonFor maps the internal kill-reason metric label to the user-visible end reason sent via EOS.
func endReasonFor(reason string) string {
	switch reason {
	case stats.ResultStoppedCPU:
		return livekit.EndReasonCPUExhausted
	default:
		return livekit.EndReasonFailure
	}
}

// StopProcess asks the handler to drain via EOS so the recording finalizes cleanly; callers must escalate to KillProcess if the handler doesn't exit.
func (pm *processManager) StopProcess(egressID string, reason string) {
	endReason := endReasonFor(reason)
	logger.Infow("stopping egress", "egressID", egressID, "reason", reason, "endReason", endReason)
	pm.mu.Lock()
	h, ok := pm.activeHandlers[egressID]
	if ok && h.killReason == "" {
		h.killReason = reason
	}
	pm.mu.Unlock()

	if !ok {
		return
	}

	if _, err := h.ipcHandlerClient.StopHandler(h.ctx, &ipc.StopHandlerRequest{
		Reason: endReason,
	}); err != nil {
		logger.Warnw("failed to send graceful stop, escalating", err, "egressID", egressID)
	}
}

func (pm *processManager) KillProcess(egressID string, reason string, err error) {
	logger.Infow("killing egress", err, "egressID", egressID)
	pm.mu.Lock()
	h, ok := pm.activeHandlers[egressID]
	if ok {
		h.killReason = reason
	}
	pm.mu.Unlock()

	if ok {
		logger.Errorw("killing handler", err, "egressID", egressID)
		h.kill(err)
	}
	logger.Infow("killing egress completed", "egressID", egressID)
}

// SetExitReason records the result the handler should be reported under when it
// finishes on its own. Unlike KillProcess it does not terminate the subprocess.
func (pm *processManager) SetExitReason(egressID string, reason string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if h, ok := pm.activeHandlers[egressID]; ok && h.killReason == "" {
		h.killReason = reason
	}
}

func (pm *processManager) GetKillReason(egressID string) string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if h, ok := pm.activeHandlers[egressID]; ok {
		return h.killReason
	}
	return ""
}

func (pm *processManager) ProcessFinished(egressID string) {
	logger.Debugw("process finished", "egressID", egressID)

	if pm.deregisterTopics != nil {
		pm.deregisterTopics(egressID)
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	p, ok := pm.activeHandlers[egressID]
	if ok {
		logger.Debugw("process finished, closing handler client", "egressID", egressID)
		p.ipcHandlerClient.Close()
		p.closed.Break()
	}

	delete(pm.activeHandlers, egressID)
	logger.Debugw("process finished, deleted from active handlers", "egressID", egressID)
}

type Process struct {
	ctx              context.Context
	handlerID        string
	req              *rpc.StartEgressRequest
	info             *livekit.EgressInfo
	cmd              *exec.Cmd
	ipcHandlerClient *ipc.EgressHandlerClientWrapper
	ready            chan struct{}
	closed           core.Fuse
	killReason       string
	metricsFinalized atomic.Bool

	metricsMu                deadlock.Mutex
	lastAccumulatableMetrics []*dto.MetricFamily
}

func (p *Process) storeAccumulatableMetrics(metrics []*dto.MetricFamily) {
	p.metricsMu.Lock()
	defer p.metricsMu.Unlock()
	p.lastAccumulatableMetrics = metrics
}

func (p *Process) getAccumulatableMetrics() []*dto.MetricFamily {
	p.metricsMu.Lock()
	defer p.metricsMu.Unlock()
	return p.lastAccumulatableMetrics
}

// Gather implements prometheus.Gatherer, pulling live metrics from the handler
// over IPC. It returns empty once the handler's metrics are finalized so its
// values aren't counted both live and in the service accumulator.
func (p *Process) Gather() ([]*dto.MetricFamily, error) {
	if p.metricsFinalized.Load() {
		return make([]*dto.MetricFamily, 0), nil
	}

	// Avoid deadlock if the IPC doesn't return as the MetricsService lock is held when Gather is called
	ctx, cancel := context.WithTimeout(context.Background(), metricsGatherTimeout)
	defer cancel()

	metricsResponse, err := p.ipcHandlerClient.GetMetrics(ctx, &ipc.MetricsRequest{})
	if err != nil {
		if !p.closed.IsBroken() {
			logger.Warnw("failed to obtain metrics from handler", err, "egressID", p.req.EgressId)
		}
		return make([]*dto.MetricFamily, 0), nil // don't return an error, just skip this handler
	}

	m, err := deserializeMetrics(p.info.EgressId, metricsResponse.Metrics)
	if err != nil {
		return m, err
	}

	accumulable, _ := splitForAccumulator(m)
	p.storeAccumulatableMetrics(accumulable)

	return m, nil
}

func (p *Process) kill(e error) {
	p.closed.Once(func() {
		if _, err := p.ipcHandlerClient.KillEgress(p.ctx, &ipc.KillEgressRequest{
			Error: e.Error(),
		}); err != nil {
			if err = p.cmd.Process.Signal(syscall.SIGINT); err != nil {
				logger.Errorw("failed to kill Process", err, "egressID", p.req.EgressId)
			}
		}
	})
}
