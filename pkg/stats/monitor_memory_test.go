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

// helper to create float64 pointer
func ptrFloat64(v float64) *float64 {
	return &v
}

func TestCheckMemoryAdmissionLocked_Legacy(t *testing.T) {
	m := &Monitor{
		cpuCostConfig: &config.CPUCostConfig{
			MaxMemory:        10,            // 10 GB
			MemoryCost:       1,             // 1 GB per request
			MemoryHeadroomGB: ptrFloat64(1), // 1 GB headroom
			MemorySource:     config.MemorySourceProcRSS,
		},
		memoryUsage: 5, // 5 GB current usage
	}

	// 5 + 0 (pending) + 1 (cost) + 1 (headroom) = 7 < 10, should accept
	reject, _ := m.checkMemoryAdmissionLocked()
	require.False(t, reject)

	// Increase usage to trigger rejection
	m.memoryUsage = 8 // 8 + 0 + 1 + 1 = 10 >= 10, should reject
	reject, reason := m.checkMemoryAdmissionLocked()
	require.True(t, reject)
	require.Equal(t, "memory", reason)
}

func TestCheckMemoryAdmissionLocked_CgroupTotal(t *testing.T) {
	m := &Monitor{
		cpuCostConfig: &config.CPUCostConfig{
			MaxMemory:        10,
			MemoryCost:       1,
			MemoryHeadroomGB: ptrFloat64(1),
			MemorySource:     config.MemorySourceCgroupTotal,
		},
		cgroupTotalBytes: 5 * gb, // 5 GB
		cgroupOK:         true,
	}

	// 5 + 0 + 1 + 1 = 7 < 10, should accept
	reject, _ := m.checkMemoryAdmissionLocked()
	require.False(t, reject)

	// Increase to trigger rejection
	m.cgroupTotalBytes = 8 * gb // 8 + 0 + 1 + 1 = 10 >= 10
	reject, reason := m.checkMemoryAdmissionLocked()
	require.True(t, reject)
	require.Equal(t, "memory_cgroup_total", reason)
}

func TestCheckMemoryAdmissionLocked_CgroupWorkingSet(t *testing.T) {
	m := &Monitor{
		cpuCostConfig: &config.CPUCostConfig{
			MaxMemory:        10,
			MemoryCost:       1,
			MemoryHeadroomGB: ptrFloat64(1),
			MemorySource:     config.MemorySourceCgroupWorkingSet,
		},
		cgroupWorkingSetBytes: 5 * gb,
		cgroupOK:              true,
	}

	// Working set is 5 GB, should accept
	reject, _ := m.checkMemoryAdmissionLocked()
	require.False(t, reject)

	// Increase working set to trigger rejection
	m.cgroupWorkingSetBytes = 8 * gb
	reject, reason := m.checkMemoryAdmissionLocked()
	require.True(t, reject)
	require.Equal(t, "memory_cgroup_workingset", reason)
}

func TestCheckMemoryAdmissionLocked_FallbackToProcRSS(t *testing.T) {
	m := &Monitor{
		cpuCostConfig: &config.CPUCostConfig{
			MaxMemory:        10,
			MemoryCost:       1,
			MemoryHeadroomGB: ptrFloat64(1),
			MemorySource:     config.MemorySourceCgroupTotal,
		},
		memoryUsage: 5,
		cgroupOK:    false, // cgroup not available
	}

	// Should fall back to proc_rss
	reject, _ := m.checkMemoryAdmissionLocked()
	require.False(t, reject) // 5 + 0 + 1 + 1 = 7 < 10

	m.memoryUsage = 8
	reject, reason := m.checkMemoryAdmissionLocked()
	require.True(t, reject)
	require.Equal(t, "memory", reason) // proc_rss reason
}

func TestCheckMemoryAdmissionLocked_NoMaxMemory(t *testing.T) {
	m := &Monitor{
		cpuCostConfig: &config.CPUCostConfig{
			MaxMemory:    0, // disabled
			MemorySource: config.MemorySourceCgroupTotal,
		},
		memoryUsage: 100,
	}

	// Should not reject when MaxMemory is 0
	reject, _ := m.checkMemoryAdmissionLocked()
	require.False(t, reject)
}

func TestCheckMemoryAdmissionLocked_WithPendingMemory(t *testing.T) {
	m := &Monitor{
		cpuCostConfig: &config.CPUCostConfig{
			MaxMemory:        10,
			MemoryCost:       1,
			MemoryHeadroomGB: ptrFloat64(1),
			MemorySource:     config.MemorySourceProcRSS,
		},
		memoryUsage: 5,
	}

	// Add pending memory
	m.pendingMemoryUsage.Store(2) // 5 + 2 + 1 + 1 = 9 < 10
	reject, _ := m.checkMemoryAdmissionLocked()
	require.False(t, reject)

	m.pendingMemoryUsage.Store(3) // 5 + 3 + 1 + 1 = 10 >= 10
	reject, reason := m.checkMemoryAdmissionLocked()
	require.True(t, reject)
	require.Equal(t, "memory", reason)
}

func TestCheckProcRSSMemoryAdmission(t *testing.T) {
	m := &Monitor{
		memoryUsage: 5,
	}

	// Various scenarios
	reject, _ := m.checkProcRSSMemoryAdmission(0, 1, 1, 10)
	require.False(t, reject) // 5 + 0 + 1 + 1 = 7 < 10

	reject, _ = m.checkProcRSSMemoryAdmission(2, 1, 1, 10)
	require.False(t, reject) // 5 + 2 + 1 + 1 = 9 < 10

	reject, reason := m.checkProcRSSMemoryAdmission(3, 1, 1, 10)
	require.True(t, reject) // 5 + 3 + 1 + 1 = 10 >= 10
	require.Equal(t, "memory", reason)
}
