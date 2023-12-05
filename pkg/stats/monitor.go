// Copyright 2023 LiveKit, Inc.
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
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

type Monitor struct {
	cpuCostConfig config.CPUCostConfig

	promCPULoad     prometheus.Gauge
	promCPULoadV2   prometheus.Gauge
	promProcCPULoad *prometheus.GaugeVec
	requestGauge    *prometheus.GaugeVec

	cpuStats  *utils.CPUStats
	procStats *utils.CPUStats

	pendingCPUs atomic.Float64

	mu       sync.Mutex
	requests atomic.Int32
}

const cpuHoldDuration = time.Second * 5

func NewMonitor(conf *config.ServiceConfig) *Monitor {
	return &Monitor{
		cpuCostConfig: conf.CPUCostConfig,
	}
}

func (m *Monitor) Start(
	conf *config.ServiceConfig,
	isIdle func() float64,
	canAcceptRequest func() float64,
	getEgressIDs func(map[int]float64) map[string]float64,
) error {
	cpuStats, err := utils.NewCPUStats(func(idle float64) {
		m.promCPULoad.Set(1 - idle/m.cpuStats.NumCPU())
	})
	if err != nil {
		return err
	}

	procStats, err := utils.NewProcCPUStats(func(idle float64, usage map[int]float64) {
		m.promCPULoadV2.Set(1 - idle/m.cpuStats.NumCPU())
		egressIDs := getEgressIDs(usage)
		for egressID, cpuUsage := range egressIDs {
			m.promProcCPULoad.With(prometheus.Labels{"egress_id": egressID}).Set(cpuUsage)
		}
	})
	if err != nil {
		return err
	}

	m.cpuStats = cpuStats
	m.procStats = procStats

	if err = m.checkCPUConfig(); err != nil {
		return err
	}

	promNodeAvailable := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "available",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "cluster_id": conf.ClusterID},
	}, isIdle)

	promCanAcceptRequest := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "can_accept_request",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "cluster_id": conf.ClusterID},
	}, canAcceptRequest)

	m.promCPULoad = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "node",
		Name:        "cpu_load",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "node_type": "EGRESS", "cluster_id": conf.ClusterID},
	})

	m.promCPULoadV2 = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "node",
		Name:        "cpu_load_v2",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "cluster_id": conf.ClusterID},
	})

	m.promProcCPULoad = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "handler_cpu_load",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "cluster_id": conf.ClusterID},
	}, []string{"egress_id"})

	m.requestGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "requests",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "cluster_id": conf.ClusterID},
	}, []string{"type"})

	prometheus.MustRegister(promNodeAvailable, promCanAcceptRequest, m.promCPULoad, m.promCPULoadV2, m.promProcCPULoad, m.requestGauge)

	return nil
}

func (m *Monitor) checkCPUConfig() error {
	requirements := []float64{
		m.cpuCostConfig.RoomCompositeCpuCost,
		m.cpuCostConfig.WebCpuCost,
		m.cpuCostConfig.ParticipantCpuCost,
		m.cpuCostConfig.TrackCompositeCpuCost,
		m.cpuCostConfig.TrackCpuCost,
	}
	sort.Float64s(requirements)

	recommendedMinimum := requirements[len(requirements)-1]
	if recommendedMinimum < 3 {
		recommendedMinimum = 3
	}

	if m.cpuStats.NumCPU() < requirements[0] {
		logger.Errorw("not enough cpu", nil,
			"minimumCpu", requirements[0],
			"recommended", recommendedMinimum,
			"available", m.cpuStats.NumCPU(),
		)
		return errors.New("not enough cpu")
	}

	if m.cpuStats.NumCPU() < requirements[len(requirements)-1] {
		logger.Errorw("not enough cpu for some egress types", nil,
			"minimumCpu", requirements[len(requirements)-1],
			"recommended", recommendedMinimum,
			"available", m.cpuStats.NumCPU(),
		)
	}

	logger.Infow(fmt.Sprintf("cpu available: %f max cost: %f", m.cpuStats.NumCPU(), requirements[len(requirements)-1]))

	return nil
}

func (m *Monitor) GetCPULoad() float64 {
	return (m.cpuStats.NumCPU() - m.cpuStats.GetCPUIdle()) / m.cpuStats.NumCPU() * 100
}

func (m *Monitor) GetRequestCount() int {
	return int(m.requests.Load())
}

func (m *Monitor) CanAcceptRequest(req *rpc.StartEgressRequest) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.canAcceptRequest(req)
}

func (m *Monitor) canAcceptRequest(req *rpc.StartEgressRequest) bool {
	accept := false

	total := m.cpuStats.NumCPU()
	available := m.cpuStats.GetCPUIdle() - m.pendingCPUs.Load()

	logger.Debugw("cpu check",
		"total", total,
		"available", available,
		"active_requests", m.requests,
	)

	if m.requests.Load() == 0 {
		// if no requests, use total
		available = total
	} else {
		// if already running requests, cap usage at 90%
		available -= 0.9 * total
	}

	switch req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		accept = available >= m.cpuCostConfig.RoomCompositeCpuCost
	case *rpc.StartEgressRequest_Web:
		accept = available >= m.cpuCostConfig.WebCpuCost
	case *rpc.StartEgressRequest_Participant:
		accept = available >= m.cpuCostConfig.ParticipantCpuCost
	case *rpc.StartEgressRequest_TrackComposite:
		accept = available >= m.cpuCostConfig.TrackCompositeCpuCost
	case *rpc.StartEgressRequest_Track:
		accept = available >= m.cpuCostConfig.TrackCpuCost
	}

	return accept
}

func (m *Monitor) AcceptRequest(req *rpc.StartEgressRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.canAcceptRequest(req) {
		return errors.ErrResourceExhausted
	}

	var cpuHold float64
	switch req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		cpuHold = m.cpuCostConfig.RoomCompositeCpuCost
	case *rpc.StartEgressRequest_Web:
		cpuHold = m.cpuCostConfig.WebCpuCost
	case *rpc.StartEgressRequest_Participant:
		cpuHold = m.cpuCostConfig.ParticipantCpuCost
	case *rpc.StartEgressRequest_TrackComposite:
		cpuHold = m.cpuCostConfig.TrackCompositeCpuCost
	case *rpc.StartEgressRequest_Track:
		cpuHold = m.cpuCostConfig.TrackCpuCost
	}

	m.requests.Inc()
	m.pendingCPUs.Add(cpuHold)
	time.AfterFunc(cpuHoldDuration, func() { m.pendingCPUs.Sub(cpuHold) })
	return nil
}

func (m *Monitor) EgressStarted(req *rpc.StartEgressRequest) {
	switch req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		m.requestGauge.With(prometheus.Labels{"type": types.RequestTypeRoomComposite}).Add(1)
	case *rpc.StartEgressRequest_Web:
		m.requestGauge.With(prometheus.Labels{"type": types.RequestTypeWeb}).Add(1)
	case *rpc.StartEgressRequest_Participant:
		m.requestGauge.With(prometheus.Labels{"type": types.RequestTypeParticipant}).Add(1)
	case *rpc.StartEgressRequest_TrackComposite:
		m.requestGauge.With(prometheus.Labels{"type": types.RequestTypeTrackComposite}).Add(1)
	case *rpc.StartEgressRequest_Track:
		m.requestGauge.With(prometheus.Labels{"type": types.RequestTypeTrack}).Add(1)
	}
}

func (m *Monitor) EgressEnded(req *rpc.StartEgressRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		m.requestGauge.With(prometheus.Labels{"type": types.RequestTypeRoomComposite}).Sub(1)
	case *rpc.StartEgressRequest_Web:
		m.requestGauge.With(prometheus.Labels{"type": types.RequestTypeWeb}).Sub(1)
	case *rpc.StartEgressRequest_Participant:
		m.requestGauge.With(prometheus.Labels{"type": types.RequestTypeParticipant}).Sub(1)
	case *rpc.StartEgressRequest_TrackComposite:
		m.requestGauge.With(prometheus.Labels{"type": types.RequestTypeTrackComposite}).Sub(1)
	case *rpc.StartEgressRequest_Track:
		m.requestGauge.With(prometheus.Labels{"type": types.RequestTypeTrack}).Sub(1)
	}

	m.requests.Dec()
}

func (m *Monitor) EgressAborted(_ *rpc.StartEgressRequest) {
	m.requests.Dec()
}
