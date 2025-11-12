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
	"os"
	"os/exec"
	"path"
	"syscall"
	"time"

	"github.com/frostbyte73/core"
	"github.com/linkdata/deadlock"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
)

const launchTimeout = 10 * time.Second

type ProcessManager struct {
	mu             deadlock.RWMutex
	activeHandlers map[string]*Process
}

func NewProcessManager() *ProcessManager {
	return &ProcessManager{
		activeHandlers: make(map[string]*Process),
	}
}

func (pm *ProcessManager) Launch(
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

	if err = cmd.Start(); err != nil {
		logger.Errorw("could not launch process", err)
		return err
	}

	select {
	case <-p.ready:
		return nil

	case <-time.After(launchTimeout):
		logger.Warnw("no response from handler", nil, "egressID", info.EgressId)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.ErrEgressNotFound
	}
}

func (pm *ProcessManager) GetContext(egressID string) context.Context {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if p, ok := pm.activeHandlers[egressID]; ok {
		return p.ctx
	}

	return context.Background()
}

func (pm *ProcessManager) AlreadyExists(egressID string) bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	_, ok := pm.activeHandlers[egressID]
	return ok
}

func (pm *ProcessManager) HandlerStarted(egressID string) error {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if p, ok := pm.activeHandlers[egressID]; ok {
		close(p.ready)
		return nil
	}

	return errors.ErrEgressNotFound
}

func (pm *ProcessManager) GetActiveEgressIDs() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	egressIDs := make([]string, 0, len(pm.activeHandlers))
	for egressID := range pm.activeHandlers {
		egressIDs = append(egressIDs, egressID)
	}

	return egressIDs
}

func (pm *ProcessManager) GetStatus(info map[string]interface{}) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for _, h := range pm.activeHandlers {
		info[h.req.EgressId] = h.req.Request
	}
}

func (pm *ProcessManager) GetGatherers() []prometheus.Gatherer {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	handlers := make([]prometheus.Gatherer, 0, len(pm.activeHandlers))
	for _, p := range pm.activeHandlers {
		handlers = append(handlers, p)
	}

	return handlers
}

func (pm *ProcessManager) GetGRPCClient(egressID string) (ipc.EgressHandlerClient, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	h, ok := pm.activeHandlers[egressID]
	if !ok {
		return nil, errors.ErrEgressNotFound
	}
	return h.ipcHandlerClient, nil
}

func (pm *ProcessManager) KillAll() {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for _, h := range pm.activeHandlers {
		h.kill(errors.ErrShuttingDown)
	}
}

func (pm *ProcessManager) AbortProcess(egressID string, err error) {
	logger.Debugw("aborting egress", err, "egressID", egressID)
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if h, ok := pm.activeHandlers[egressID]; ok {
		logger.Warnw("aborting handler", err, "egressID", egressID)
		h.kill(err)
		h.ipcHandlerClient.Close()
		delete(pm.activeHandlers, egressID)
	}
	logger.Debugw("aborting egress completed", "egressID", egressID)
}

func (pm *ProcessManager) KillProcess(egressID string, err error) {
	logger.Debugw("killing egress", err, "egressID", egressID)
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if h, ok := pm.activeHandlers[egressID]; ok {
		logger.Errorw("killing handler", err, "egressID", egressID)
		h.kill(err)
	}
	logger.Debugw("killing egress completed", "egressID", egressID)
}

func (pm *ProcessManager) ProcessFinished(egressID string) {
	logger.Debugw("process finished", "egressID", egressID)
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
}

// Gather implements the prometheus.Gatherer interface on server-side to allow aggregation of handler ms
func (p *Process) Gather() ([]*dto.MetricFamily, error) {
	// Get the ms from the handler via IPC
	metricsResponse, err := p.ipcHandlerClient.GetMetrics(context.Background(), &ipc.MetricsRequest{})
	if err != nil {
		if !p.closed.IsBroken() {
			logger.Warnw("failed to obtain ms from handler", err, "egressID", p.req.EgressId)
		}
		return make([]*dto.MetricFamily, 0), nil // don't return an error, just skip this handler
	}

	// Parse the result to match the Gatherer interface
	return deserializeMetrics(p.info.EgressId, metricsResponse.Metrics)
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
