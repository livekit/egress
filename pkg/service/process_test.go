package service

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"

	"github.com/livekit/egress/pkg/errors"
)

func TestLaunchExitsBeforeReady(t *testing.T) {
	pm := NewProcessManager()

	start := time.Now()
	wait, err := launchTestProcess(t, pm, "EG_exits_before_ready", exec.Command("false"))
	require.Nil(t, wait)
	require.Less(t, time.Since(start), 2*time.Second)

	var psrpcErr psrpc.Error
	require.True(t, errors.As(err, &psrpcErr))
	require.Equal(t, psrpc.FailedPrecondition, psrpcErr.Code())
}

func TestLaunchTimesOut(t *testing.T) {
	pm := NewProcessManager()

	wait, err := launchTestProcess(t, pm, "EG_times_out", exec.Command("sleep", "30"))
	require.Nil(t, wait)
	require.ErrorIs(t, err, errors.ErrHandlerFailedToStart)
}

func TestLaunchReadyThenExit(t *testing.T) {
	pm := NewProcessManager()
	egressID := "EG_ready_then_exit"

	go func() {
		assert.Eventually(t, func() bool { return pm.AlreadyExists(egressID) }, time.Second, 10*time.Millisecond)
		assert.NoError(t, pm.HandlerStarted(egressID))
	}()

	wait, err := launchTestProcess(t, pm, egressID, exec.Command("sleep", "1"))
	require.NoError(t, err)
	require.NoError(t, wait())
}

func launchTestProcess(t *testing.T, pm ProcessManager, egressID string, cmd *exec.Cmd) (func() error, error) {
	t.Cleanup(func() { pm.ProcessFinished(egressID) })
	return pm.Launch(
		context.Background(),
		utils.NewGuid("EGH_"),
		&rpc.StartEgressRequest{EgressId: egressID},
		&livekit.EgressInfo{EgressId: egressID},
		cmd,
	)
}
