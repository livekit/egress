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
	"github.com/prometheus/common/expfmt"
	"golang.org/x/exp/maps"
	"net"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/frostbyte73/core"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/protocol/utils"

	dto "github.com/prometheus/client_model/go"
)

type Process struct {
	handlerID  string
	req        *rpc.StartEgressRequest
	info       *livekit.EgressInfo
	cmd        *exec.Cmd
	grpcClient ipc.EgressHandlerClient
	closed     core.Fuse
}

func NewProcess(
	handlerID string,
	req *rpc.StartEgressRequest,
	info *livekit.EgressInfo,
	cmd *exec.Cmd,
	tmpDir string,
) (*Process, error) {
	p := &Process{
		handlerID: handlerID,
		req:       req,
		info:      info,
		cmd:       cmd,
		closed:    core.NewFuse(),
	}

	socketAddr := getSocketAddress(tmpDir)
	conn, err := grpc.Dial(socketAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(_ context.Context, addr string) (net.Conn, error) {
			return net.Dial(network, addr)
		}),
	)
	if err != nil {
		logger.Errorw("could not dial grpc handler", err)
		return nil, err
	}
	p.grpcClient = ipc.NewEgressHandlerClient(conn)

	return p, nil
}

func (s *Service) launchHandler(req *rpc.StartEgressRequest, info *livekit.EgressInfo) error {
	_, span := tracer.Start(context.Background(), "Service.launchHandler")
	defer span.End()

	handlerID := utils.NewGuid("EGH_")
	p := &config.PipelineConfig{
		BaseConfig: s.conf.BaseConfig,
		HandlerID:  handlerID,
		TmpDir:     path.Join(os.TempDir(), handlerID),
	}

	confString, err := yaml.Marshal(p)
	if err != nil {
		span.RecordError(err)
		logger.Errorw("could not marshal config", err)
		return err
	}

	reqString, err := protojson.Marshal(req)
	if err != nil {
		span.RecordError(err)
		logger.Errorw("could not marshal request", err)
		return err
	}

	cmd := exec.Command("egress",
		"run-handler",
		"--config", string(confString),
		"--request", string(reqString),
	)
	cmd.Dir = "/"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err = cmd.Start(); err != nil {
		span.RecordError(err)
		logger.Errorw("could not launch process", err)
		return err
	}

	s.EgressStarted(req)

	h, err := NewProcess(handlerID, req, info, cmd, p.TmpDir)
	if err != nil {
		span.RecordError(err)
		return err
	}

	s.AddHandler(req.EgressId, h)
	return nil
}

func (s *Service) AddHandler(egressID string, h *Process) {
	s.mu.Lock()
	s.activeHandlers[egressID] = h
	s.mu.Unlock()

	go s.awaitCleanup(h)
}

func (s *Service) awaitCleanup(p *Process) {
	if err := p.cmd.Wait(); err != nil {
		now := time.Now().UnixNano()
		p.info.UpdatedAt = now
		p.info.EndedAt = now
		p.info.Status = livekit.EgressStatus_EGRESS_FAILED
		p.info.Error = "internal error"
		sendUpdate(context.Background(), s.ioClient, p.info)
		s.Stop(false)
	}

	s.EgressEnded(p.req)
	p.closed.Break()

	s.mu.Lock()
	delete(s.activeHandlers, p.req.EgressId)
	s.mu.Unlock()
}

func (s *Service) IsIdle() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.activeHandlers) == 0
}

func (s *Service) getGRPCClient(egressID string) (ipc.EgressHandlerClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	h, ok := s.activeHandlers[egressID]
	if !ok {
		return nil, errors.ErrEgressNotFound
	}
	return h.grpcClient, nil
}

func (s *Service) KillAll() {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, h := range s.activeHandlers {
		if !h.closed.IsBroken() {
			if err := h.cmd.Process.Signal(syscall.SIGINT); err != nil {
				logger.Errorw("failed to kill Process", err, "egressID", h.req.EgressId)
			}
		}
	}
}

func getSocketAddress(handlerTmpDir string) string {
	return path.Join(handlerTmpDir, "service_rpc.sock")
}

// Gather implements the prometheus.Gatherer interface on server-side to allow aggregation of handler metrics
func (p *Process) Gather() ([]*dto.MetricFamily, error) {
	// Get the metrics from the handler via IPC
	logger.Debugw("gathering metrics from handler process", "handlerID", p.handlerID)
	metricsResponse, err := p.grpcClient.GetMetrics(context.Background(), &ipc.MetricsRequest{})
	if err != nil {
		logger.Errorw("Error obtaining metrics from handler", err)
		return make([]*dto.MetricFamily, 0), err
	}
	// Parse the result to match the Gatherer interface
	parser := &expfmt.TextParser{}
	families, err := parser.TextToMetricFamilies(strings.NewReader(metricsResponse.Metrics))
	if err != nil {
		logger.Errorw("Error parsing metrics from handler", err)
		return make([]*dto.MetricFamily, 0), err
	}

	// Add an egress_id label to every metric all the families, if it doesn't already have one
	applyDefaultLabel(p, families)

	return maps.Values(families), nil
}

func applyDefaultLabel(p *Process, families map[string]*dto.MetricFamily) {
	egressLabelPair := &dto.LabelPair{
		Name:  StringPtr("egress_id"),
		Value: &p.req.EgressId,
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

func StringPtr(v string) *string { return &v }
