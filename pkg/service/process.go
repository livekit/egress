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
	"os/exec"
	"syscall"

	"github.com/frostbyte73/core"
	dto "github.com/prometheus/client_model/go"

	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
)

type Process struct {
	ctx              context.Context
	handlerID        string
	req              *rpc.StartEgressRequest
	info             *livekit.EgressInfo
	cmd              *exec.Cmd
	ipcHandlerClient ipc.EgressHandlerClient
	ready            chan struct{}
	closed           core.Fuse
}

func NewProcess(
	ctx context.Context,
	handlerID string,
	req *rpc.StartEgressRequest,
	info *livekit.EgressInfo,
	cmd *exec.Cmd,
	tmpDir string,
) (*Process, error) {
	ipcClient, err := ipc.NewHandlerClient(tmpDir)
	if err != nil {
		return nil, err
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

	return p, nil
}

// Gather implements the prometheus.Gatherer interface on server-side to allow aggregation of handler metrics
func (p *Process) Gather() ([]*dto.MetricFamily, error) {
	// Get the metrics from the handler via IPC
	metricsResponse, err := p.ipcHandlerClient.GetMetrics(context.Background(), &ipc.MetricsRequest{})
	if err != nil {
		logger.Warnw("failed to obtain metrics from handler", err, "egress_id", p.req.EgressId)
		return make([]*dto.MetricFamily, 0), nil // don't return an error, just skip this handler
	}

	// Parse the result to match the Gatherer interface
	return deserializeMetrics(p.info.EgressId, metricsResponse.Metrics)
}

func (p *Process) kill() {
	if !p.closed.IsBroken() {
		if err := p.cmd.Process.Signal(syscall.SIGINT); err != nil {
			logger.Errorw("failed to kill Process", err, "egressID", p.req.EgressId)
		}
	}
}

func applyDefaultLabel(egressID string, families map[string]*dto.MetricFamily) {
	egressIDLabel := "egress_id"
	egressLabelPair := &dto.LabelPair{
		Name:  &egressIDLabel,
		Value: &egressID,
	}
	for _, family := range families {
		for _, metric := range family.Metric {
			if metric.Label == nil {
				metric.Label = make([]*dto.LabelPair, 0)
			}
			found := false
			for _, label := range metric.Label {
				if label.GetName() == "egress_id" {
					found = true
					break
				}
			}
			if !found {
				metric.Label = append(metric.Label, egressLabelPair)
			}
		}
	}
}
