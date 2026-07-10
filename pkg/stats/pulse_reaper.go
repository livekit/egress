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
	"context"
	"strings"
	"time"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"

	"github.com/livekit/egress/pkg/pipeline/source/pulse"
)

const (
	pulseReapInterval   = 30 * time.Second
	pulseReapCmdTimeout = 10 * time.Second
)

// runPulseSinkReaper unloads null-sinks left on the shared pulse daemon by handlers that
// exited without cleanup, before they accumulate and hit the pulse client limit.
func (m *Monitor) runPulseSinkReaper() {
	firstOrphaned := make(map[string]time.Time)
	listFailing := false

	// a grace shorter than the default interval would otherwise be quantized back up to it
	ticker := time.NewTicker(min(pulseReapInterval, m.pulseSinkReapGrace))
	defer ticker.Stop()

	for range ticker.C {
		info, err := listPulse()
		if err != nil {
			if !listFailing {
				listFailing = true
				logger.Warnw("pulse sink reaper: skipping check, list failed", err)
			}
			continue
		}
		listFailing = false

		toReap, egressSinks := planPulseSinkReaps(info.Sinks, m.activeEgressIDs(), firstOrphaned, m.pulseSinkReapGrace, time.Now())
		m.promPulseSinks.Set(float64(egressSinks))

		for _, sink := range toReap {
			if err = unloadPulseSink(sink); err != nil {
				logger.Warnw("failed to unload orphaned pulse sink", err, "egressID", sink.Name, "module", sink.OwnerModule)
			} else {
				logger.Infow("unloaded orphaned pulse sink", "egressID", sink.Name, "module", sink.OwnerModule)
			}
		}
	}
}

func listPulse() (*pulse.PulseInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pulseReapCmdTimeout)
	defer cancel()
	return pulse.ListContext(ctx)
}

func unloadPulseSink(sink pulse.Device) error {
	ctx, cancel := context.WithTimeout(context.Background(), pulseReapCmdTimeout)
	defer cancel()
	return pulse.UnloadModule(ctx, sink.OwnerModule)
}

// planPulseSinkReaps selects egress-owned sinks (named after their EgressId) whose egress
// is no longer tracked by the monitor and which have stayed orphaned for the full grace
// period, covering the window between EgressEnded and the handler unloading its own sink.
func planPulseSinkReaps(
	sinks []pulse.Device,
	activeEgressIDs map[string]bool,
	firstOrphaned map[string]time.Time,
	grace time.Duration,
	now time.Time,
) (toReap []pulse.Device, egressSinks int) {
	orphaned := make(map[string]bool, len(sinks))

	for _, sink := range sinks {
		if !strings.HasPrefix(sink.Name, utils.EgressPrefix) {
			continue
		}
		egressSinks++
		if activeEgressIDs[sink.Name] {
			continue
		}

		orphaned[sink.Name] = true
		since, seen := firstOrphaned[sink.Name]
		if !seen {
			firstOrphaned[sink.Name] = now
		} else if now.Sub(since) >= grace {
			toReap = append(toReap, sink)
			delete(firstOrphaned, sink.Name)
		}
	}

	for name := range firstOrphaned {
		if !orphaned[name] {
			delete(firstOrphaned, name)
		}
	}

	return
}

func (m *Monitor) activeEgressIDs() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	ids := make(map[string]bool, len(m.pending)+len(m.procStats))
	for egressID := range m.pending {
		ids[egressID] = true
	}
	for _, ps := range m.procStats {
		ids[ps.egressID] = true
	}
	return ids
}
