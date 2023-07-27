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
	"net"
	"os"
	"os/exec"
	"path"
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

func (s *Service) awaitCleanup(h *Process) {
	if err := h.cmd.Wait(); err != nil {
		now := time.Now().UnixNano()
		h.info.UpdatedAt = now
		h.info.EndedAt = now
		h.info.Status = livekit.EgressStatus_EGRESS_FAILED
		h.info.Error = "internal error"
		sendUpdate(context.Background(), s.ioClient, h.info)
		s.Stop(false)
	}

	s.EgressEnded(h.req)
	h.closed.Break()

	s.mu.Lock()
	delete(s.activeHandlers, h.req.EgressId)
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
