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
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/protocol/utils"
)

func (s *Service) StartEgress(ctx context.Context, req *rpc.StartEgressRequest) (*livekit.EgressInfo, error) {
	ctx, span := tracer.Start(ctx, "Service.StartEgress")
	defer span.End()

	if err := s.AcceptRequest(req); err != nil {
		return nil, err
	}

	logger.Infow("request received", "egressID", req.EgressId)

	p, err := config.GetValidatedPipelineConfig(s.conf, req)
	if err != nil {
		s.EgressAborted(req)
		return nil, err
	}

	_, err = s.ioClient.CreateEgress(ctx, p.Info)
	if err != nil {
		s.EgressAborted(req)
		return nil, err
	}

	requestType, outputType := egress.GetTypes(p.Info.Request)
	logger.Infow("request validated",
		"egressID", req.EgressId,
		"requestType", requestType,
		"outputType", outputType,
		"room", p.Info.RoomName,
		"request", p.Info.Request,
	)

	err = s.launchHandler(req, p.Info)
	if err != nil {
		s.EgressAborted(req)
		return nil, err
	}

	return p.Info, nil
}

func (s *Service) StartEgressAffinity(_ context.Context, req *rpc.StartEgressRequest) float32 {
	if !s.CanAcceptRequest(req) {
		// cannot accept
		return -1
	}

	if s.GetRequestCount() == 0 {
		// group multiple track and track composite requests.
		// if this instance is idle and another is already handling some, the request will go to that server.
		// this avoids having many instances with one track request each, taking availability from room composite.
		return 0.5
	} else {
		// already handling a request and has available cpu
		return 1
	}
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

	s.EgressStarted(req)

	h, err := NewProcess(context.Background(), handlerID, req, info, cmd, p.TmpDir)
	if err != nil {
		span.RecordError(err)
		return err
	}

	err = s.AddHandler(req.EgressId, h)
	if err != nil {
		span.RecordError(err)
		return err
	}

	return nil
}

func (s *Service) AddHandler(egressID string, p *Process) error {
	s.mu.Lock()
	s.activeHandlers[egressID] = p
	s.mu.Unlock()

	if err := p.cmd.Start(); err != nil {
		logger.Errorw("could not launch process", err)
		return err
	}

	select {
	case <-p.ready:
		s.UpdatePID(egressID, p.cmd.Process.Pid)
		go func() {
			err := p.cmd.Wait()
			s.processEnded(p, err)
		}()

	case <-time.After(10 * time.Second):
		_ = p.cmd.Process.Kill()
		s.processEnded(p, errors.ErrEgressNotFound)
	}

	return nil
}

func (s *Service) processEnded(p *Process, err error) {
	if err != nil {
		now := time.Now().UnixNano()
		p.info.UpdatedAt = now
		p.info.EndedAt = now
		p.info.Status = livekit.EgressStatus_EGRESS_FAILED
		if p.info.Error == "" {
			p.info.Error = "internal error"
		}
		_, _ = s.ioClient.UpdateEgress(p.ctx, p.info)
		if p.info.Error == "internal error" {
			s.Stop(false)
		}
	}

	avgCPU, maxCPU := s.EgressEnded(p.req)
	if maxCPU > 0 {
		_, _ = s.ioClient.UpdateMetrics(p.ctx, &rpc.UpdateMetricsRequest{
			Info:        p.info,
			AvgCpuUsage: float32(avgCPU),
			MaxCpuUsage: float32(maxCPU),
		})
	}

	p.closed.Break()

	s.mu.Lock()
	delete(s.activeHandlers, p.req.EgressId)
	s.mu.Unlock()
}

func (s *Service) ListActiveEgress(ctx context.Context, _ *rpc.ListActiveEgressRequest) (*rpc.ListActiveEgressResponse, error) {
	ctx, span := tracer.Start(ctx, "Service.ListActiveEgress")
	defer span.End()

	s.mu.RLock()
	defer s.mu.RUnlock()

	egressIDs := make([]string, 0, len(s.activeHandlers))
	for egressID := range s.activeHandlers {
		egressIDs = append(egressIDs, egressID)
	}

	return &rpc.ListActiveEgressResponse{
		EgressIds: egressIDs,
	}, nil
}
