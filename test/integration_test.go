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

//go:build integration

package test

import (
	"embed"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/protocol/redis"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

var (
	//go:embed templates
	templateEmbedFs embed.FS
)

func TestEgress(t *testing.T) {
	r := NewRunner(t)

	rfs, err := fs.Sub(templateEmbedFs, "templates")
	require.NoError(t, err)

	// rpc client and server
	rc, err := redis.GetRedisClient(r.Redis)
	require.NoError(t, err)
	bus := psrpc.NewRedisMessageBus(rc)

	ioClient, err := service.NewIOClient(bus)
	require.NoError(t, err)

	svc, err := service.NewService(r.ServiceConfig, ioClient)
	require.NoError(t, err)

	psrpcServer, err := rpc.NewEgressInternalServer(svc, bus)
	require.NoError(t, err)
	svc.Register(psrpcServer)

	r.Run(t, svc, bus, rfs)
}
