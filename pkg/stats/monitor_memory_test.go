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

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
)

func TestCheckMemoryAdmissionLocked_Legacy(t *testing.T) {
	m := &Monitor{
		cpuCostConfig: &config.CPUCostConfig{
			MaxMemory:    10, // 10 GB
			MemoryCost:   1,  // 1 GB per request
			MemorySource: config.MemorySourceProcRSS,
		},
		memoryUsage: 5, // 5 GB current usage
	}

	// 5 + 0 (pending) + 1 (cost) + 1 (headroom) = 7 < 10, should accept
	reject, _ := m.checkMemoryAdmissionLocked(m.cpuCostConfig.MemoryCost)
	require.False(t, reject)

	// Increase usage to trigger rejection
	m.memoryUsage = 8 // 8 + 0 + 1 + 1 = 10 >= 10, should reject
	reject, reason := m.checkMemoryAdmissionLocked(m.cpuCostConfig.MemoryCost)
	require.True(t, reject)
	require.Equal(t, "memory", reason)
}

func TestCheckMemoryAdmissionLocked_CgroupWorkingSet(t *testing.T) {
	m := &Monitor{
		cpuCostConfig: &config.CPUCostConfig{
			MaxMemory:    10,
			MemoryCost:   1,
			MemorySource: config.MemorySourceCgroup,
		},
		cgroupUsageBytes: 5 * gb,
		cgroupOK:         true,
	}

	// Working set is 5 GB, should accept
	reject, _ := m.checkMemoryAdmissionLocked(m.cpuCostConfig.MemoryCost)
	require.False(t, reject)

	// Increase working set to trigger rejection
	m.cgroupUsageBytes = 8 * gb
	reject, reason := m.checkMemoryAdmissionLocked(m.cpuCostConfig.MemoryCost)
	require.True(t, reject)
	require.Equal(t, "memory_cgroup", reason)
}

func TestCheckMemoryAdmissionLocked_FallbackToProcRSS(t *testing.T) {
	m := &Monitor{
		cpuCostConfig: &config.CPUCostConfig{
			MaxMemory:    10,
			MemoryCost:   1,
			MemorySource: config.MemorySourceCgroup,
		},
		memoryUsage: 5,
		cgroupOK:    false, // cgroup not available
	}

	// Should fall back to proc_rss
	reject, _ := m.checkMemoryAdmissionLocked(m.cpuCostConfig.MemoryCost)
	require.False(t, reject) // 5 + 0 + 1 + 1 = 7 < 10

	m.memoryUsage = 8
	reject, reason := m.checkMemoryAdmissionLocked(m.cpuCostConfig.MemoryCost)
	require.True(t, reject)
	require.Equal(t, "memory", reason) // proc_rss reason
}

func TestCheckMemoryAdmissionLocked_NoMaxMemory(t *testing.T) {
	m := &Monitor{
		cpuCostConfig: &config.CPUCostConfig{
			MaxMemory:    0, // disabled
			MemorySource: config.MemorySourceCgroup,
		},
		memoryUsage: 100,
	}

	// Should not reject when MaxMemory is 0
	reject, _ := m.checkMemoryAdmissionLocked(m.cpuCostConfig.MemoryCost)
	require.False(t, reject)
}

func TestCheckMemoryAdmissionLocked_WithPendingMemory(t *testing.T) {
	m := &Monitor{
		cpuCostConfig: &config.CPUCostConfig{
			MaxMemory:    10,
			MemoryCost:   1,
			MemorySource: config.MemorySourceProcRSS,
		},
		memoryUsage: 5,
	}

	// Add pending memory
	m.pendingMemoryUsage.Store(2) // 5 + 2 + 1 + 1 = 9 < 10
	reject, _ := m.checkMemoryAdmissionLocked(m.cpuCostConfig.MemoryCost)
	require.False(t, reject)

	m.pendingMemoryUsage.Store(3) // 5 + 3 + 1 + 1 = 10 >= 10
	reject, reason := m.checkMemoryAdmissionLocked(m.cpuCostConfig.MemoryCost)
	require.True(t, reject)
	require.Equal(t, "memory", reason)
}

// GetAdmissibleMemory reports what AcceptRequest compares a request's memory cost with, so a
// request fits the reported value exactly when the node would admit it.
func TestGetAdmissibleMemory(t *testing.T) {
	for _, c := range []struct {
		name   string
		source config.MemorySource
		cgroup bool
	}{
		{"proc rss", config.MemorySourceProcRSS, false},
		{"cgroup", config.MemorySourceCgroup, true},
		{"cgroup unreadable", config.MemorySourceCgroup, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := &Monitor{
				cpuCostConfig:    &config.CPUCostConfig{MaxMemory: 10, MemorySource: c.source},
				memoryUsage:      4,
				cgroupUsageBytes: 5 * gb,
				cgroupOK:         c.cgroup,
			}
			m.pendingMemoryUsage.Store(2)

			inUse := 4.0
			if c.cgroup {
				inUse = 5
			}
			admissible := m.GetAdmissibleMemory()
			require.Equal(t, 10-inUse-2-memoryHeadroomGB, admissible)

			for _, cost := range []float64{0.5, admissible - 0.01, admissible + 0.5} {
				reject, _ := m.checkMemoryAdmissionLocked(cost)
				require.Equal(t, cost >= admissible, reject, "cost %v", cost)
			}
		})
	}
}

// The memory check compares the sum of usage, pending memory, the request's cost and headroom with
// MaxMemory. Rearranging the sum rounds differently at a fractional limit.
func TestCheckMemoryAdmissionLockedRoundsAsSum(t *testing.T) {
	for _, c := range []struct {
		inUse, pending, cost, max float64
		reject                    bool
	}{
		// 1.5 + 0 + 1.7 + 1 reaches 4.2, but 4.2 - 1.5 - 0 - 1 is 1.7000000000000002.
		{inUse: 1.5, cost: 1.7, max: 4.2, reject: true},
		// 1.25 + 0.8 + 0.8 + 1 rounds below 3.85, but 3.85 - 1.25 - 0.8 - 1 is exactly 0.8.
		{inUse: 1.25, pending: 0.8, cost: 0.8, max: 3.85, reject: false},
	} {
		m := &Monitor{
			cpuCostConfig: &config.CPUCostConfig{MaxMemory: c.max, MemorySource: config.MemorySourceProcRSS},
			memoryUsage:   c.inUse,
		}
		m.pendingMemoryUsage.Store(c.pending)
		reject, _ := m.checkMemoryAdmissionLocked(c.cost)
		require.Equal(t, c.reject, reject, "%+v", c)
	}
}
