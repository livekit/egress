// Copyright 2026 LiveKit, Inc.
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

	"google.golang.org/grpc"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

// HandlerRPCProxy hosts the per-egress EgressHandler psrpc topics in the
// service process and forwards requests to the handler subprocess over IPC,
// so handlers never connect to the message bus.
type HandlerRPCProxy struct {
	pm     ProcessManager
	server rpc.EgressHandlerServer
}

func NewHandlerRPCProxy(pm ProcessManager, bus psrpc.MessageBus) (*HandlerRPCProxy, error) {
	p := &HandlerRPCProxy{pm: pm}
	server, err := rpc.NewEgressHandlerServer(p, bus)
	if err != nil {
		return nil, err
	}
	p.server = server
	return p, nil
}

// RegisterEgress is invoked by the process manager during Launch, once the
// handler's IPC client exists, so incoming requests can always resolve a
// client. Calls made before the handler's IPC listener is up wait on the
// connection (bounded by the request deadline) instead of failing fast.
func (p *HandlerRPCProxy) RegisterEgress(egressID string) error {
	if err := p.server.RegisterUpdateStreamTopic(egressID); err != nil {
		return err
	}
	if err := p.server.RegisterStopEgressTopic(egressID); err != nil {
		p.DeregisterEgress(egressID)
		return err
	}
	if err := p.server.RegisterUpdateEgressTopic(egressID); err != nil {
		p.DeregisterEgress(egressID)
		return err
	}
	return nil
}

func (p *HandlerRPCProxy) DeregisterEgress(egressID string) {
	p.server.DeregisterUpdateStreamTopic(egressID)
	p.server.DeregisterStopEgressTopic(egressID)
	p.server.DeregisterUpdateEgressTopic(egressID)
}

func (p *HandlerRPCProxy) UpdateStream(ctx context.Context, req *livekit.UpdateStreamRequest) (*livekit.EgressInfo, error) {
	client, err := p.pm.GetGRPCClient(req.EgressId)
	if err != nil {
		return nil, err
	}
	return client.UpdateStream(ctx, req, grpc.WaitForReady(true))
}

func (p *HandlerRPCProxy) UpdateEgress(ctx context.Context, req *livekit.UpdateEgressRequest) (*livekit.EgressInfo, error) {
	client, err := p.pm.GetGRPCClient(req.EgressId)
	if err != nil {
		return nil, err
	}
	return client.UpdateEgress(ctx, req, grpc.WaitForReady(true))
}

func (p *HandlerRPCProxy) StopEgress(ctx context.Context, req *livekit.StopEgressRequest) (*livekit.EgressInfo, error) {
	client, err := p.pm.GetGRPCClient(req.EgressId)
	if err != nil {
		return nil, err
	}
	return client.StopEgress(ctx, req, grpc.WaitForReady(true))
}

func (p *HandlerRPCProxy) Shutdown() {
	p.server.Shutdown()
}

func (p *HandlerRPCProxy) Kill() {
	p.server.Kill()
}
