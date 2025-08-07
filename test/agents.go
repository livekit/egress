// Copyright 2025 LiveKit, Inc.
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
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func (r *Runner) launchAgents(t *testing.T) {
	cmd := exec.Command("python3", "agent.py", "dev")
	cmd.Dir = "/agents"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Start()
	require.NoError(t, err)

	agentsClient := lksdk.NewAgentDispatchServiceClient(r.WsUrl, r.ApiKey, r.ApiSecret)
	res, err := agentsClient.CreateDispatch(context.Background(), &livekit.CreateAgentDispatchRequest{
		AgentName: "egress-integration",
		Room:      r.RoomName,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = agentsClient.DeleteDispatch(context.Background(), &livekit.DeleteAgentDispatchRequest{
			DispatchId: res.Id,
			Room:       r.RoomName,
		})
	})
}
