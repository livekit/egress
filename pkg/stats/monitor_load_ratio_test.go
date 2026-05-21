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

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
)

func getGaugeValue(g *prometheus.GaugeVec, label string) float64 {
	m := &dto.Metric{}
	gauge, _ := g.GetMetricWithLabelValues(label)
	_ = gauge.Write(m)
	return m.GetGauge().GetValue()
}

func newTestMonitorForLoadRatio(cpuUtil, maxMem float64, maxPulse int) *Monitor {
	loadRatio := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "livekit",
		Name:      "load_ratio_test",
	}, []string{"type"})

	return &Monitor{
		cpuCostConfig: &config.CPUCostConfig{
			MaxCpuUtilization: cpuUtil,
			MaxMemory:         maxMem,
			MemorySource:      config.MemorySourceProcRSS,
			MaxPulseClients:   maxPulse,
		},
		promLoadRatio: loadRatio,
	}
}

func TestLoadRatio_CPU(t *testing.T) {
	m := newTestMonitorForLoadRatio(0.8, 0, 0)

	// 40% CPU load / 80% max = 0.5 ratio
	m.updateLoadRatios(0.4)
	require.InDelta(t, 0.5, getGaugeValue(m.promLoadRatio, "cpu"), 0.001)

	// 80% CPU load / 80% max = 1.0 ratio (at limit)
	m.updateLoadRatios(0.8)
	require.InDelta(t, 1.0, getGaugeValue(m.promLoadRatio, "cpu"), 0.001)

	// 96% CPU load / 80% max = 1.2 ratio (over limit)
	m.updateLoadRatios(0.96)
	require.InDelta(t, 1.2, getGaugeValue(m.promLoadRatio, "cpu"), 0.001)

	// 0% CPU load = 0 ratio
	m.updateLoadRatios(0)
	require.InDelta(t, 0.0, getGaugeValue(m.promLoadRatio, "cpu"), 0.001)
}

func TestLoadRatio_CPU_Disabled(t *testing.T) {
	m := newTestMonitorForLoadRatio(0, 0, 0) // MaxCpuUtilization = 0

	m.updateLoadRatios(0.5)
	// Should not set anything when disabled
	require.InDelta(t, 0.0, getGaugeValue(m.promLoadRatio, "cpu"), 0.001)
}

func TestLoadRatio_Memory_ProcRSS(t *testing.T) {
	m := newTestMonitorForLoadRatio(0, 10, 0) // 10 GB max
	m.memoryUsage = 5                          // 5 GB used

	m.updateLoadRatios(0)
	require.InDelta(t, 0.5, getGaugeValue(m.promLoadRatio, "memory"), 0.001)

	m.memoryUsage = 9
	m.updateLoadRatios(0)
	require.InDelta(t, 0.9, getGaugeValue(m.promLoadRatio, "memory"), 0.001)

	m.memoryUsage = 0
	m.updateLoadRatios(0)
	require.InDelta(t, 0.0, getGaugeValue(m.promLoadRatio, "memory"), 0.001)
}

func TestLoadRatio_Memory_Cgroup(t *testing.T) {
	m := newTestMonitorForLoadRatio(0, 10, 0)
	m.cpuCostConfig.MemorySource = config.MemorySourceCgroup
	m.cgroupOK = true
	m.cgroupUsageBytes = 5 * uint64(gb) // 5 GB

	m.updateLoadRatios(0)
	require.InDelta(t, 0.5, getGaugeValue(m.promLoadRatio, "memory"), 0.001)
}

func TestLoadRatio_Memory_CgroupFallback(t *testing.T) {
	m := newTestMonitorForLoadRatio(0, 10, 0)
	m.cpuCostConfig.MemorySource = config.MemorySourceCgroup
	m.cgroupOK = false // cgroup unavailable
	m.memoryUsage = 7  // falls back to proc RSS

	m.updateLoadRatios(0)
	require.InDelta(t, 0.7, getGaugeValue(m.promLoadRatio, "memory"), 0.001)
}

func TestLoadRatio_Memory_Disabled(t *testing.T) {
	m := newTestMonitorForLoadRatio(0, 0, 0) // MaxMemory = 0

	m.memoryUsage = 100
	m.updateLoadRatios(0)
	require.InDelta(t, 0.0, getGaugeValue(m.promLoadRatio, "memory"), 0.001)
}

func TestLoadRatio_AllDimensions(t *testing.T) {
	m := newTestMonitorForLoadRatio(0.8, 20, 0) // skip pulse
	m.memoryUsage = 14                           // 14/20 = 0.7

	m.updateLoadRatios(0.6) // 0.6/0.8 = 0.75

	cpuRatio := getGaugeValue(m.promLoadRatio, "cpu")
	memRatio := getGaugeValue(m.promLoadRatio, "memory")

	require.InDelta(t, 0.75, cpuRatio, 0.001)
	require.InDelta(t, 0.7, memRatio, 0.001)

	// Memory is the bottleneck at 0.7, CPU at 0.75
	// Autoscaler uses max(cpu, memory) per pod — CPU wins here
	require.Greater(t, cpuRatio, memRatio)
}
