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

const testGrace = 30 * time.Second

func TestPulseSinkReaper_SkipsActiveAndForeignSinks(t *testing.T) {
	sinks := []pulse.Device{
		{Name: "auto_null", OwnerModule: 1},
		{Name: "EG_running", OwnerModule: 2},
		{Name: "not_an_egress_sink", OwnerModule: 3},
	}
	active := map[string]bool{"EG_running": true}
	firstOrphaned := map[string]time.Time{}

	now := time.Now()
	for _, tick := range []time.Duration{0, testGrace, 2 * testGrace} {
		toReap, egressSinks := planPulseSinkReaps(sinks, active, firstOrphaned, testGrace, now.Add(tick))
		require.Empty(t, toReap)
		require.Equal(t, 1, egressSinks)
	}
	require.Empty(t, firstOrphaned)
}

func TestPulseSinkReaper_ReapsOrphanAfterGrace(t *testing.T) {
	sinks := []pulse.Device{{Name: "EG_orphan", OwnerModule: 7}}
	active := map[string]bool{}
	firstOrphaned := map[string]time.Time{}
	now := time.Now()

	// first sighting only starts the clock
	toReap, egressSinks := planPulseSinkReaps(sinks, active, firstOrphaned, testGrace, now)
	require.Empty(t, toReap)
	require.Equal(t, 1, egressSinks)
	require.Contains(t, firstOrphaned, "EG_orphan")

	// still within grace
	toReap, _ = planPulseSinkReaps(sinks, active, firstOrphaned, testGrace, now.Add(testGrace/2))
	require.Empty(t, toReap)

	// grace elapsed
	toReap, _ = planPulseSinkReaps(sinks, active, firstOrphaned, testGrace, now.Add(testGrace))
	require.Len(t, toReap, 1)
	require.Equal(t, "EG_orphan", toReap[0].Name)
	require.Equal(t, 7, toReap[0].OwnerModule)
	require.Empty(t, firstOrphaned)

	// if the unload failed and the sink is still present, the clock restarts
	toReap, _ = planPulseSinkReaps(sinks, active, firstOrphaned, testGrace, now.Add(testGrace+time.Second))
	require.Empty(t, toReap)
	require.Contains(t, firstOrphaned, "EG_orphan")
}

func TestPulseSinkReaper_EgressActiveAgainClearsTracking(t *testing.T) {
	sinks := []pulse.Device{{Name: "EG_restarted", OwnerModule: 4}}
	firstOrphaned := map[string]time.Time{}
	now := time.Now()

	planPulseSinkReaps(sinks, map[string]bool{}, firstOrphaned, testGrace, now)
	require.Contains(t, firstOrphaned, "EG_restarted")

	// the egress shows up as active before the grace elapses (e.g. retried request)
	toReap, _ := planPulseSinkReaps(sinks, map[string]bool{"EG_restarted": true}, firstOrphaned, testGrace, now.Add(2*testGrace))
	require.Empty(t, toReap)
	require.Empty(t, firstOrphaned)
}

func TestPulseSinkReaper_SinkGoneClearsTracking(t *testing.T) {
	firstOrphaned := map[string]time.Time{}
	now := time.Now()

	planPulseSinkReaps([]pulse.Device{{Name: "EG_cleaned", OwnerModule: 5}}, map[string]bool{}, firstOrphaned, testGrace, now)
	require.Contains(t, firstOrphaned, "EG_cleaned")

	// the handler unloaded its own sink during the grace window
	toReap, egressSinks := planPulseSinkReaps(nil, map[string]bool{}, firstOrphaned, testGrace, now.Add(time.Second))
	require.Empty(t, toReap)
	require.Zero(t, egressSinks)
	require.Empty(t, firstOrphaned)
}
