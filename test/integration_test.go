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

	ioClient, err := rpc.NewIOInfoClient("test_io_client", bus)
	require.NoError(t, err)

	svc, err := service.NewService(r.ServiceConfig, ioClient)
	require.NoError(t, err)

	psrpcServer, err := rpc.NewEgressInternalServer(r.NodeID, svc, bus)
	require.NoError(t, err)
	svc.Register(psrpcServer)

	r.Run(t, svc, bus, rfs)
}
