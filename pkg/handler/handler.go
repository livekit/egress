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

package handler

import (
	"context"
	"path"

	"github.com/frostbyte73/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"google.golang.org/grpc"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/psrpc"
)

type Handler struct {
	ipc.UnimplementedEgressHandlerServer

	conf             *config.PipelineConfig
	controller       *pipeline.Controller
	rpcServer        rpc.EgressHandlerServer
	ipcHandlerServer *grpc.Server
	ipcServiceClient ipc.EgressServiceClient
	initialized      core.Fuse
	kill             core.Fuse
}

func NewHandler(conf *config.PipelineConfig, bus psrpc.MessageBus) (*Handler, error) {
	// Register all GO process metrics
	prometheus.Unregister(prometheus.NewGoCollector())
	prometheus.MustRegister(collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsAll)))

	ipcClient, err := ipc.NewServiceClient(path.Join(config.TmpDir, conf.NodeID))
	if err != nil {
		return nil, err
	}

	h := &Handler{
		conf:             conf,
		ipcHandlerServer: grpc.NewServer(),
		ipcServiceClient: ipcClient,
	}

	ipc.RegisterEgressHandlerServer(h.ipcHandlerServer, h)
	if err = ipc.StartHandlerListener(h.ipcHandlerServer, path.Join(config.TmpDir, conf.HandlerID)); err != nil {
		return nil, err
	}

	rpcServer, err := rpc.NewEgressHandlerServer(h, bus)
	if err != nil {
		return nil, err
	}
	if err = rpcServer.RegisterUpdateStreamTopic(conf.Info.EgressId); err != nil {
		return nil, err
	}
	if err = rpcServer.RegisterStopEgressTopic(conf.Info.EgressId); err != nil {
		return nil, err
	}
	h.rpcServer = rpcServer

	_, err = h.ipcServiceClient.HandlerReady(context.Background(), &ipc.HandlerReadyRequest{EgressId: conf.Info.EgressId})
	if err != nil {
		logger.Errorw("failed to notify service", err)
		return nil, err
	}

	return h, nil
}

func (h *Handler) Run() {
	ctx, span := tracer.Start(context.Background(), "Handler.Run")
	defer span.End()

	defer func() {
		h.rpcServer.Shutdown()
		h.ipcHandlerServer.Stop()
	}()

	var err error
	h.controller, err = pipeline.New(context.Background(), h.conf, h.ipcServiceClient)
	h.initialized.Break()
	if err != nil {
		h.conf.Info.SetFailed(err)
		_, _ = h.ipcServiceClient.HandlerUpdate(context.Background(), h.conf.Info)
		return
	}

	// start egress
	res := h.controller.Run(ctx)
	m, err := h.GenerateMetrics(ctx)
	if err != nil {
		logger.Errorw("failed to generate handler metrics", err)
	}

	_, _ = h.ipcServiceClient.HandlerFinished(ctx, &ipc.HandlerFinishedRequest{
		EgressId: h.conf.Info.EgressId,
		Metrics:  m,
		Info:     res,
	})
}

func (h *Handler) Kill() {
	<-h.initialized.Watch()
	if h.controller == nil {
		return
	}
	h.controller.SendEOS(context.Background(), livekit.EndReasonKilled)
}
