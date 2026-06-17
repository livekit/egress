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

package stats

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
)

type stopCall struct {
	egressID  string
	reason    string
	endReason string
}

type killCall struct {
	egressID string
	reason   string
}

type fakeCPUService struct {
	stops []stopCall
	kills []killCall
}

func (f *fakeCPUService) IsIdle() bool        { return false }
func (f *fakeCPUService) IsDisabled() bool    { return false }
func (f *fakeCPUService) IsTerminating() bool { return false }
func (f *fakeCPUService) StopProcess(egressID, reason, endReason string) {
	f.stops = append(f.stops, stopCall{egressID, reason, endReason})
}
func (f *fakeCPUService) KillProcess(egressID, reason string, _ error) {
	f.kills = append(f.kills, killCall{egressID, reason})
}

func newCPUMonitor(svc *fakeCPUService, gracePeriodSec int) *Monitor {
	m := &Monitor{
		svc: svc,
		cpuCostConfig: &config.CPUCostConfig{
			MaxCpuUtilization: 0.8,
			CpuKillGraceSec:   gracePeriodSec,
		},
	}
	m.requests.Store(2)
	return m
}

// Drives load above the kill threshold (0.95 with the default config) minKillDuration times.
func driveHighLoad(m *Monitor, egressID string) {
	for i := 0; i < minKillDuration; i++ {
		m.checkCPUKill(0.99, 0.95, egressID)
	}
}

func TestCheckCPUKill_GracefulStopOnSustainedHighCPU(t *testing.T) {
	svc := &fakeCPUService{}
	m := newCPUMonitor(svc, 20)

	driveHighLoad(m, "EG_1")

	require.Len(t, svc.stops, 1, "graceful stop should fire after minKillDuration ticks")
	require.Equal(t, "EG_1", svc.stops[0].egressID)
	require.Equal(t, ResultStoppedCPU, svc.stops[0].reason)
	require.Equal(t, EndReasonCPUExhausted, svc.stops[0].endReason)
	require.Empty(t, svc.kills, "first breach should not hard-kill")
	require.False(t, m.cpuStopRequestedAt.IsZero(), "stop timestamp should be set")
	require.Equal(t, "EG_1", m.cpuStopEgressID)
}

func TestCheckCPUKill_EscalatesToKillAfterGracePeriod(t *testing.T) {
	svc := &fakeCPUService{}
	m := newCPUMonitor(svc, 20)

	driveHighLoad(m, "EG_1")
	require.Len(t, svc.stops, 1)

	// Within grace: another tick must not kill.
	m.checkCPUKill(0.99, 0.95, "EG_1")
	require.Empty(t, svc.kills)

	// Pretend grace elapsed.
	m.cpuStopRequestedAt = time.Now().Add(-25 * time.Second)
	m.checkCPUKill(0.99, 0.95, "EG_1")

	require.Len(t, svc.kills, 1, "hard kill must fire when grace period elapses")
	require.Equal(t, "EG_1", svc.kills[0].egressID)
	require.Equal(t, ResultKilledCPU, svc.kills[0].reason)
	require.True(t, m.cpuStopRequestedAt.IsZero(), "tracking should clear after kill")
	require.Empty(t, m.cpuStopEgressID)
}

func TestCheckCPUKill_TransientSpikesDoNotAccumulate(t *testing.T) {
	svc := &fakeCPUService{}
	m := newCPUMonitor(svc, 20)

	// 9 high ticks, then load drops, then 9 high ticks again — never reaches minKillDuration.
	for range minKillDuration - 1 {
		m.checkCPUKill(0.99, 0.95, "EG_1")
	}
	m.checkCPUKill(0.5, 0, "")
	require.Equal(t, 0, m.highCPUDuration, "sub-threshold tick must reset the counter")

	for range minKillDuration - 1 {
		m.checkCPUKill(0.99, 0.95, "EG_1")
	}

	require.Empty(t, svc.stops, "transient spikes must not trigger a stop")
	require.Empty(t, svc.kills)
}

func TestCheckCPUKill_NoActionBelowThreshold(t *testing.T) {
	svc := &fakeCPUService{}
	m := newCPUMonitor(svc, 20)

	for range 100 {
		m.checkCPUKill(0.5, 0, "")
	}

	require.Empty(t, svc.stops)
	require.Empty(t, svc.kills)
	require.Equal(t, 0, m.highCPUDuration)
}

func TestCheckCPUKill_NoActionWithSingleEgress(t *testing.T) {
	svc := &fakeCPUService{}
	m := newCPUMonitor(svc, 20)
	m.requests.Store(1)

	driveHighLoad(m, "EG_1")

	require.Empty(t, svc.stops, "single egress must not be killed even at sustained high load")
	require.Empty(t, svc.kills)
}

func TestCheckCPUKill_LoadDropDuringDrainKeepsTracking(t *testing.T) {
	svc := &fakeCPUService{}
	m := newCPUMonitor(svc, 20)

	driveHighLoad(m, "EG_1")
	require.Len(t, svc.stops, 1)

	// CPU comes down (drain is making progress), but the stop is still in-flight.
	m.checkCPUKill(0.5, 0, "")

	require.False(t, m.cpuStopRequestedAt.IsZero(), "drop in load must not cancel the in-flight stop")
	require.Equal(t, "EG_1", m.cpuStopEgressID)
}
