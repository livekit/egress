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

	"github.com/livekit/egress/pkg/pipeline/source/pulse"
)

func newTestMonitorForReap(graceSec int) *Monitor {
	return &Monitor{
		pulseSinkReapGraceSec: graceSec,
		pending:               make(map[string]*processStats),
		procStats:             make(map[int]*processStats),
		orphanedSinks:         make(map[string]time.Time),
	}
}

func TestPlanSinkReaps_IgnoresActiveAndBaseSinks(t *testing.T) {
	m := newTestMonitorForReap(30)
	m.pending["EG_active_pending"] = &processStats{egressID: "EG_active_pending"}
	m.procStats[42] = &processStats{egressID: "EG_active_running"}

	sinks := []pulse.Device{
		{Name: "auto_null", OwnerModule: 1},         // base daemon sink — never touch
		{Name: "EG_active_pending", OwnerModule: 2}, // active (pending)
		{Name: "EG_active_running", OwnerModule: 3}, // active (running)
		{Name: "some_other_sink", OwnerModule: 4},   // not an egress sink (no EG_ prefix)
	}

	now := time.Now()
	reaps := m.planSinkReapsLocked(sinks, now)
	require.Empty(t, reaps, "no orphans should be reaped")
	require.Empty(t, m.orphanedSinks, "active/base/foreign sinks should not be tracked")
}

func TestPlanSinkReaps_GracePeriodThenReap(t *testing.T) {
	m := newTestMonitorForReap(30)

	orphan := []pulse.Device{{Name: "EG_orphan", OwnerModule: 7}}

	// First sighting: start the grace timer, do not reap yet.
	t0 := time.Now()
	reaps := m.planSinkReapsLocked(orphan, t0)
	require.Empty(t, reaps)
	require.Contains(t, m.orphanedSinks, "EG_orphan")

	// Within the grace window: still no reap.
	reaps = m.planSinkReapsLocked(orphan, t0.Add(15*time.Second))
	require.Empty(t, reaps)

	// Past the grace window: reap the owning module.
	reaps = m.planSinkReapsLocked(orphan, t0.Add(31*time.Second))
	require.Len(t, reaps, 1)
	require.Equal(t, "EG_orphan", reaps[0].name)
	require.Equal(t, 7, reaps[0].module)
	require.NotContains(t, m.orphanedSinks, "EG_orphan", "tracking reset after reap")
}

func TestPlanSinkReaps_RecoveryClearsTracking(t *testing.T) {
	m := newTestMonitorForReap(30)
	orphan := []pulse.Device{{Name: "EG_flaky", OwnerModule: 9}}

	// Seen once as orphaned.
	t0 := time.Now()
	m.planSinkReapsLocked(orphan, t0)
	require.Contains(t, m.orphanedSinks, "EG_flaky")

	// Its egress becomes active again before the grace elapsed: tracking must clear so it is
	// never reaped while in use.
	m.pending["EG_flaky"] = &processStats{egressID: "EG_flaky"}
	reaps := m.planSinkReapsLocked(orphan, t0.Add(10*time.Second))
	require.Empty(t, reaps)
	require.NotContains(t, m.orphanedSinks, "EG_flaky")
}

func TestPlanSinkReaps_SinkDisappearsClearsTracking(t *testing.T) {
	m := newTestMonitorForReap(30)
	orphan := []pulse.Device{{Name: "EG_gone", OwnerModule: 5}}

	t0 := time.Now()
	m.planSinkReapsLocked(orphan, t0)
	require.Contains(t, m.orphanedSinks, "EG_gone")

	// Handler's own Close() unloaded the sink; it's no longer present. Tracking should be dropped.
	reaps := m.planSinkReapsLocked(nil, t0.Add(5*time.Second))
	require.Empty(t, reaps)
	require.Empty(t, m.orphanedSinks)
}

func TestReapOrphanedPulseSinks_DisabledByNegativeGrace(t *testing.T) {
	m := newTestMonitorForReap(-1)

	// A negative grace disables reaping: reapOrphanedPulseSinksLocked must short-circuit before
	// shelling out to pactl, so it is safe to call with no pulse daemon available.
	require.NotPanics(t, m.reapOrphanedPulseSinksLocked)
}
