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

package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/egress/pkg/service/servicefakes"
)

func TestHandlerRPCProxyServerOptions(t *testing.T) {
	const egressID = "EG_proxy_options"

	stop := func(t *testing.T, interceptorErr error) (*servicefakes.FakeProcessManager, psrpc.ErrorCode) {
		bus := psrpc.NewLocalMessageBus()
		pm := &servicefakes.FakeProcessManager{}
		pm.GetGRPCClientReturns(nil, errors.ErrEgressNotFound)

		proxy, err := service.NewHandlerRPCProxy(pm, bus, psrpc.WithServerRPCInterceptors(
			func(ctx context.Context, req proto.Message, _ psrpc.RPCInfo, handler psrpc.ServerRPCHandler) (proto.Message, error) {
				if interceptorErr != nil {
					return nil, interceptorErr
				}
				return handler(ctx, req)
			},
		))
		require.NoError(t, err)
		t.Cleanup(proxy.Kill)
		require.NoError(t, proxy.RegisterEgress(egressID))

		client, err := rpc.NewEgressHandlerClient(bus)
		require.NoError(t, err)

		_, err = client.StopEgress(context.Background(), egressID, &livekit.StopEgressRequest{EgressId: egressID})
		var psrpcErr psrpc.Error
		require.True(t, errors.As(err, &psrpcErr))
		return pm, psrpcErr.Code()
	}

	t.Run("a request rejected by an interceptor never reaches the handler", func(t *testing.T) {
		pm, code := stop(t, psrpc.NewErrorf(psrpc.PermissionDenied, "rejected"))
		require.Equal(t, psrpc.PermissionDenied, code)
		require.Zero(t, pm.GetGRPCClientCallCount())
	})

	t.Run("a request passed by an interceptor is forwarded to the handler", func(t *testing.T) {
		pm, code := stop(t, nil)
		require.Equal(t, psrpc.NotFound, code)
		require.Equal(t, 1, pm.GetGRPCClientCallCount())
	})
}
