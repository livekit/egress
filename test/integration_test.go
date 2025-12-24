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

	"github.com/livekit/egress/pkg/info"
	"github.com/livekit/egress/pkg/server"
	"github.com/livekit/protocol/redis"
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

	ioClient, err := info.NewSessionReporter(&r.ServiceConfig.BaseConfig, bus)
	require.NoError(t, err)

	svc, err := server.NewServer(r.ServiceConfig, bus, ioClient)
	require.NoError(t, err)

	r.StartServer(t, svc, bus, rfs)
	r.RunTests(t)
}
