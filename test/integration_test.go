//go:build integration

package test

import (
	"embed"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/redis"
	"github.com/livekit/psrpc"
)

var (
	//go:embed templates
	templateEmbedFs embed.FS
)

func TestEgress(t *testing.T) {
	conf := NewTestContext(t)

	// rpc client and server
	rc, err := redis.GetRedisClient(conf.Redis)
	require.NoError(t, err)
	rpcServer := egress.NewRedisRPCServer(rc)
	rpcClient := egress.NewRedisRPCClient("egress_test", rc)
	bus := psrpc.NewRedisMessageBus(rc)

	rfs, err := fs.Sub(templateEmbedFs, "templates")
	require.NoError(t, err)

	RunTestSuite(t, conf, rpcClient, rpcServer, bus, rfs)
}
