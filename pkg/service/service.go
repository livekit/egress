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
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/grpc"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/version"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
)

type Service struct {
	ipc.UnimplementedEgressServiceServer

	*stats.Monitor

	conf             *config.ServiceConfig
	psrpcServer      rpc.EgressInternalServer
	ipcServiceServer *grpc.Server
	ioClient         rpc.IOInfoClient
	promServer       *http.Server

	mu             sync.RWMutex
	activeHandlers map[string]*Process
	pendingMetrics []*dto.MetricFamily

	shutdown core.Fuse
}

func NewService(conf *config.ServiceConfig, ioClient rpc.IOInfoClient) (*Service, error) {
	s := &Service{
		Monitor:          stats.NewMonitor(conf),
		conf:             conf,
		ipcServiceServer: grpc.NewServer(),
		ioClient:         ioClient,
		activeHandlers:   make(map[string]*Process),
	}

	tmpDir := path.Join(os.TempDir(), conf.NodeID)
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return nil, err
	}

	ipc.RegisterEgressServiceServer(s.ipcServiceServer, s)
	if err := ipc.StartServiceListener(s.ipcServiceServer, tmpDir); err != nil {
		return nil, err
	}

	if conf.PrometheusPort > 0 {
		s.promServer = &http.Server{
			Addr:    fmt.Sprintf(":%d", conf.PrometheusPort),
			Handler: s.PromHandler(),
		}
	}

	if err := s.Start(s.conf,
		s.promIsIdle,
		s.promCanAcceptRequest,
		s.promIsDisabled,
		s.killProcess,
	); err != nil {
		return nil, err
	}

	if s.promServer != nil {
		promListener, err := net.Listen("tcp", s.promServer.Addr)
		if err != nil {
			return nil, err
		}
		go func() {
			_ = s.promServer.Serve(promListener)
		}()
	}

	return s, nil
}

func (s *Service) Register(psrpcServer rpc.EgressInternalServer) {
	s.psrpcServer = psrpcServer
}

func (s *Service) StartTemplatesServer(fs fs.FS) error {
	if s.conf.TemplatePort == 0 {
		logger.Debugw("templates server disabled")
		return nil
	}

	h := http.FileServer(http.FS(fs))

	mux := http.NewServeMux()
	mux.Handle("/", h)

	go func() {
		addr := fmt.Sprintf("localhost:%d", s.conf.TemplatePort)
		logger.Debugw(fmt.Sprintf("starting template server on address %s", addr))
		_ = http.ListenAndServe(addr, mux)
	}()

	return nil
}

func (s *Service) RegisterListEgress(topic string) error {
	return s.psrpcServer.RegisterListActiveEgressTopic(topic)
}

func (s *Service) Run() error {
	logger.Debugw("starting service", "version", version.Version)

	if err := s.psrpcServer.RegisterStartEgressTopic(s.conf.ClusterID); err != nil {
		return err
	}

	logger.Infow("service ready")
	<-s.shutdown.Watch()
	logger.Infow("shutting down")

	return nil
}

func (s *Service) Reset() {
	if !s.shutdown.IsBroken() {
		s.Stop(false)
	}

	s.shutdown = core.Fuse{}
}

func (s *Service) Status() ([]byte, error) {
	info := map[string]interface{}{
		"CpuLoad": s.GetCPULoad(),
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, h := range s.activeHandlers {
		info[h.req.EgressId] = h.req.Request
	}
	return json.Marshal(info)
}

func (s *Service) Stop(kill bool) {
	s.shutdown.Once(func() {
		s.psrpcServer.DeregisterStartEgressTopic(s.conf.ClusterID)
	})
	if kill {
		s.KillAll()
	}
}

func (s *Service) KillAll() {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, h := range s.activeHandlers {
		h.kill()
	}
}

func (s *Service) killProcess(egressID string, maxUsage float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if h, ok := s.activeHandlers[egressID]; ok {
		logger.Errorw("killing egress", errors.ErrCPUExhausted, "egressID", egressID, "usage", maxUsage)
		now := time.Now().UnixNano()
		h.info.Status = livekit.EgressStatus_EGRESS_FAILED
		h.info.Error = errors.ErrCPUExhausted.Error()
		h.info.UpdatedAt = now
		h.info.EndedAt = now
		h.kill()
	}
}

func (s *Service) Close() {
	for s.GetRequestCount() > 0 {
		time.Sleep(time.Second)
	}
	logger.Infow("closing server")
	s.psrpcServer.Shutdown()
}
