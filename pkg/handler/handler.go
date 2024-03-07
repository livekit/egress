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
	"strings"
	"time"

	"github.com/frostbyte73/core"
	"google.golang.org/grpc"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
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
	ioClient         rpc.IOInfoClient
	initialized      core.Fuse
	kill             core.Fuse
}

func NewHandler(conf *config.PipelineConfig, bus psrpc.MessageBus, ioClient rpc.IOInfoClient) (*Handler, error) {
	ipcClient, err := ipc.NewServiceClient(path.Join(conf.TmpDir[:strings.LastIndex(conf.TmpDir, "/")], conf.NodeID))
	if err != nil {
		return nil, err
	}

	h := &Handler{
		conf:             conf,
		ioClient:         ioClient,
		ipcHandlerServer: grpc.NewServer(),
		ipcServiceClient: ipcClient,
	}

	ipc.RegisterEgressHandlerServer(h.ipcHandlerServer, h)
	if err = ipc.StartHandlerListener(h.ipcHandlerServer, conf.TmpDir); err != nil {
		return nil, err
	}

	rpcServer, err := rpc.NewEgressHandlerServer(h, bus)
	if err != nil {
		return nil, errors.Fatal(err)
	}
	if err = rpcServer.RegisterUpdateStreamTopic(conf.Info.EgressId); err != nil {
		return nil, errors.Fatal(err)
	}
	if err = rpcServer.RegisterStopEgressTopic(conf.Info.EgressId); err != nil {
		return nil, errors.Fatal(err)
	}
	h.rpcServer = rpcServer

	_, err = h.ipcServiceClient.HandlerReady(context.Background(), &ipc.HandlerReadyRequest{EgressId: conf.Info.EgressId})
	if err != nil {
		logger.Errorw("failed to notify service", err)
		return nil, err
	}

	h.controller, err = pipeline.New(context.Background(), conf, h.ioClient)
	h.initialized.Break()
	if err != nil {
		if !errors.IsFatal(err) {
			// user error, send update
			now := time.Now().UnixNano()
			conf.Info.Status = livekit.EgressStatus_EGRESS_FAILED
			conf.Info.Error = err.Error()
			conf.Info.UpdatedAt = now
			conf.Info.EndedAt = now
			_, _ = h.ioClient.UpdateEgress(context.Background(), conf.Info)
		}
		return nil, err
	}

	return h, nil
}

func (h *Handler) Run() error {
	ctx, span := tracer.Start(context.Background(), "Handler.Run")
	defer span.End()

	// start egress
	result := make(chan *livekit.EgressInfo, 1)
	go func() {
		result <- h.controller.Run(ctx)
	}()

	kill := h.kill.Watch()
	for {
		select {
		case <-kill:
			// kill signal received
			h.controller.SendEOS(ctx)

		case res := <-result:
			// recording finished
			_, _ = h.ioClient.UpdateEgress(ctx, res)

			m, err := h.GenerateMetrics(ctx)
			if err == nil {
				h.ipcServiceClient.HandlerShuttingDown(ctx, &ipc.HandlerShuttingDownRequest{
					EgressId: h.conf.Info.EgressId,
					Metrics:  m,
				})
			} else {
				logger.Errorw("failed generating handler metrics", err)
			}

			h.rpcServer.Shutdown()
			h.ipcHandlerServer.Stop()
			return nil
		}
	}
}

func (h *Handler) Kill() {
	h.kill.Break()
}
