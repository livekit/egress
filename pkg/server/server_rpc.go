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

package server

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path"
	"syscall"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/logging"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
	"go.opentelemetry.io/otel"
)

var (
	tracer = otel.Tracer("github.com/livekit/egress/pkg/server")
)

func (s *Server) StartEgress(ctx context.Context, req *rpc.StartEgressRequest) (*livekit.EgressInfo, error) {
	s.activeRequests.Inc()

	ctx, span := tracer.Start(ctx, "Service.StartEgress")
	defer span.End()

	if s.IsDisabled() {
		s.activeRequests.Dec()
		return nil, errors.ErrShuttingDown
	}
	if s.AlreadyExists(req.EgressId) {
		s.activeRequests.Dec()
		return nil, errors.ErrEgressAlreadyExists
	}
	if err := s.monitor.AcceptRequest(req, s.conf.EnableRoomCompositeSDKSource); err != nil {
		s.activeRequests.Dec()
		return nil, err
	}

	logger.Infow("request received", "egressID", req.EgressId)

	p, err := config.GetValidatedPipelineConfig(s.conf, req)
	if err != nil {
		s.monitor.EgressAborted(req)
		s.activeRequests.Dec()
		return nil, err
	}

	requestType, outputType := egress.GetTypes(p.Info.Request)
	logger.Infow("request validated",
		"egressID", req.EgressId,
		"requestType", requestType,
		"sourceType", p.Info.SourceType,
		"outputType", outputType,
		"room", p.Info.RoomName,
		"request", p.Info.Request,
	)

	errChan := s.ioClient.CreateEgress(ctx, p.Info)
	launchErr := s.launchProcess(req, p.Info)
	createErr := <-errChan

	switch {
	case launchErr != nil && createErr != nil:
		s.processEnded(req, p.Info, nil)
		return nil, launchErr

	case launchErr != nil:
		s.processEnded(req, p.Info, launchErr)
		return nil, launchErr

	case createErr != nil:
		// launched but failed to save - abort and return error
		p.Info.Error = createErr.Error()
		p.Info.ErrorCode = int32(http.StatusInternalServerError)
		s.AbortProcess(req.EgressId, createErr)
		return nil, createErr

	default:
		return p.Info, nil
	}
}

func (s *Server) launchProcess(req *rpc.StartEgressRequest, info *livekit.EgressInfo) error {
	_, span := tracer.Start(context.Background(), "Service.launchProcess")
	defer span.End()

	s.monitor.EgressStarted(req)

	handlerID := utils.NewGuid("EGH_")
	p := &config.PipelineConfig{
		BaseConfig: s.conf.BaseConfig,
		HandlerID:  handlerID,
		TmpDir:     path.Join(config.TmpDir, req.EgressId),
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

	l := logging.NewHandlerLogger(handlerID, req.EgressId)
	cmd.Stdout = l
	cmd.Stderr = l
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err = s.Launch(context.Background(), handlerID, req, info, cmd); err != nil {
		return err
	}

	s.monitor.UpdatePID(info.EgressId, cmd.Process.Pid)
	go func() {
		err = cmd.Wait()
		s.processEnded(req, info, err)
	}()
	return nil
}

func (s *Server) processEnded(req *rpc.StartEgressRequest, info *livekit.EgressInfo, err error) {
	if err != nil {
		// should only happen if process failed catashrophically
		now := time.Now().UnixNano()
		info.UpdatedAt = now
		info.EndedAt = now
		info.Status = livekit.EgressStatus_EGRESS_FAILED
		if info.Error == "" {
			info.Error = err.Error()
			info.ErrorCode = int32(http.StatusInternalServerError)
		}
		_ = s.ioClient.UpdateEgress(context.Background(), info)

		logger.Errorw("process failed", err, "egressID", info.EgressId)
	}

	avgCPU, maxCPU, maxMemory := s.monitor.EgressEnded(req)
	if maxCPU > 0 {
		logger.Debugw("egress metrics",
			"egressID", info.EgressId,
			"avgCPU", avgCPU,
			"maxCPU", maxCPU,
			"maxMemory", maxMemory,
		)
	}

	// Make sure we delete all the handler context regardless of the handler termination status
	tmpDir := path.Join(config.TmpDir, req.EgressId)
	os.RemoveAll(tmpDir)

	s.ProcessFinished(info.EgressId)
	s.activeRequests.Dec()
}

func (s *Server) StartEgressAffinity(_ context.Context, req *rpc.StartEgressRequest) float32 {
	if s.IsDisabled() || !s.monitor.CanAcceptRequest(req, s.conf.EnableRoomCompositeSDKSource) {
		// cannot accept
		return -1
	}

	if s.activeRequests.Load() == 0 {
		// group multiple track and track composite requests.
		// if this instance is idle and another is already handling some, the request will go to that server.
		// this avoids having many instances with one track request each, taking availability from room composite.
		return 0.5
	}
	// already handling a request and has available cpu
	return 1
}

func (s *Server) ListActiveEgress(ctx context.Context, _ *rpc.ListActiveEgressRequest) (*rpc.ListActiveEgressResponse, error) {
	_, span := tracer.Start(ctx, "Service.ListActiveEgress")
	defer span.End()

	return &rpc.ListActiveEgressResponse{
		EgressIds: s.GetActiveEgressIDs(),
	}, nil
}
