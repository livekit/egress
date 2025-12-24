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
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"time"

	"github.com/frostbyte73/core"
	"go.uber.org/atomic"
	"google.golang.org/grpc"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/info"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/version"
)

type Server struct {
	ipc.UnimplementedEgressServiceServer

	conf *config.ServiceConfig

	*service.ProcessManager
	*service.MetricsService
	*service.DebugService
	monitor *stats.Monitor

	psrpcServer      rpc.EgressInternalServer
	ipcServiceServer *grpc.Server
	promServer       *http.Server
	ioClient         info.SessionReporter

	activeRequests atomic.Int32
	terminating    core.Fuse
	shutdown       core.Fuse
}

func NewServer(conf *config.ServiceConfig, bus psrpc.MessageBus, ioClient info.SessionReporter) (*Server, error) {
	pm := service.NewProcessManager()

	s := &Server{
		conf:             conf,
		ProcessManager:   pm,
		MetricsService:   service.NewMetricsService(pm),
		DebugService:     service.NewDebugService(pm),
		ipcServiceServer: grpc.NewServer(),
		ioClient:         ioClient,
	}

	ioClient.SetWatchdogHandler(func() {
		logger.Errorw("shutting down server on io client watchdog trigger", errors.New("io client failure"))
		s.Shutdown(false, false)
	})

	monitor, err := stats.NewMonitor(conf, s)
	if err != nil {
		return nil, err
	}
	s.monitor = monitor

	if conf.DebugHandlerPort > 0 {
		s.StartDebugHandlers(conf.DebugHandlerPort)
	}

	if conf.PrometheusPort > 0 {
		s.promServer = &http.Server{
			Addr:    fmt.Sprintf(":%d", conf.PrometheusPort),
			Handler: s.PromHandler(),
		}

		promListener, err := net.Listen("tcp", s.promServer.Addr)
		if err != nil {
			return nil, err
		}
		go func() {
			_ = s.promServer.Serve(promListener)
		}()
	}

	ipcSvcDir := path.Join(config.TmpDir, s.conf.NodeID)
	if err = os.MkdirAll(ipcSvcDir, 0755); err != nil {
		return nil, err
	}

	ipc.RegisterEgressServiceServer(s.ipcServiceServer, s)
	if err := ipc.StartServiceListener(s.ipcServiceServer, ipcSvcDir); err != nil {
		return nil, err
	}

	psrpcServer, err := rpc.NewEgressInternalServer(s, bus)
	if err != nil {
		return nil, err
	}
	if err = psrpcServer.RegisterListActiveEgressTopic(""); err != nil {
		return nil, err
	}
	s.psrpcServer = psrpcServer

	return s, nil
}

func (s *Server) StartTemplatesServer(fs fs.FS) error {
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

func (s *Server) Run() error {
	logger.Debugw("starting service", "version", version.Version)

	if err := s.psrpcServer.RegisterStartEgressTopic(s.conf.ClusterID); err != nil {
		return err
	}

	logger.Infow("service ready")
	<-s.shutdown.Watch()
	logger.Infow("draining")
	s.Drain()
	logger.Infow("service stopped")
	return nil
}

func (s *Server) Status() ([]byte, error) {
	status := map[string]interface{}{
		"CpuLoad": s.monitor.GetAvailableCPU(),
	}
	s.GetStatus(status)
	return json.Marshal(status)
}

func (s *Server) IsIdle() bool {
	return s.activeRequests.Load() == 0
}

func (s *Server) IsDisabled() bool {
	return s.shutdown.IsBroken() || !s.ioClient.IsHealthy()
}

func (s *Server) IsTerminating() bool {
	return s.terminating.IsBroken()
}

func (s *Server) Shutdown(terminating, kill bool) {
	if terminating {
		s.terminating.Break()
	}
	s.shutdown.Once(func() {
		s.psrpcServer.DeregisterStartEgressTopic(s.conf.ClusterID)
	})
	if kill {
		s.KillAll()
	}
}

func (s *Server) Drain() {
	for !s.IsIdle() {
		time.Sleep(time.Second)
	}

	s.psrpcServer.Shutdown()
	logger.Infow("draining io client")
	s.ioClient.Drain()
}
