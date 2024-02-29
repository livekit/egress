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
	cpuCostConfig *config.CPUCostConfig

	promCPULoad  prometheus.Gauge
	requestGauge *prometheus.GaugeVec

	cpuStats *utils.CPUStats
	requests atomic.Int32

	mu              sync.Mutex
	highCPUDuration int
	killProcess     func(string, float64)
	pending         map[string]*processStats
	procStats       map[int]*processStats
}

type processStats struct {
	egressID string

	pendingUsage float64
	lastUsage    float64
	allowedUsage float64

	totalCPU   float64
	cpuCounter int
	maxCPU     float64
}

const (
	cpuHoldDuration      = time.Second * 30
	defaultKillThreshold = 0.95
	minKillDuration      = 10
)

func NewMonitor(conf *config.ServiceConfig) *Monitor {
	return &Monitor{
		cpuCostConfig: conf.CPUCostConfig,
		pending:       make(map[string]*processStats),
		procStats:     make(map[int]*processStats),
	}
}

func (m *Monitor) Start(
	conf *config.ServiceConfig,
	isIdle func() float64,
	canAcceptRequest func() float64,
	isDisabled func() float64,
	killProcess func(string, float64),
) error {
	m.killProcess = killProcess

	procStats, err := utils.NewProcCPUStats(m.updateEgressStats)
	if err != nil {
		return err
	}
	m.cpuStats = procStats

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

	promIsDisabled := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "is_disabled",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "cluster_id": conf.ClusterID},
	}, isDisabled)

	m.promCPULoad = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "node",
		Name:        "cpu_load",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "node_type": "EGRESS", "cluster_id": conf.ClusterID},
	})

	m.requestGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "requests",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "cluster_id": conf.ClusterID},
	}, []string{"type"})

	prometheus.MustRegister(promNodeAvailable, promCanAcceptRequest, promIsDisabled, m.promCPULoad, m.requestGauge)

	return nil
}

func (m *Monitor) checkCPUConfig() error {
	requirements := []float64{
		m.cpuCostConfig.RoomCompositeCpuCost,
		m.cpuCostConfig.AudioRoomCompositeCpuCost,
		m.cpuCostConfig.WebCpuCost,
		m.cpuCostConfig.AudioWebCpuCost,
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

func (m *Monitor) UpdatePID(egressID string, pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ps := m.pending[egressID]
	delete(m.pending, egressID)

	if existing := m.procStats[pid]; existing != nil {
		ps.maxCPU = existing.maxCPU
		ps.totalCPU = existing.totalCPU
		ps.cpuCounter = existing.cpuCounter
	}
	m.procStats[pid] = ps
}

func (m *Monitor) updateEgressStats(idle float64, usage map[int]float64) {
	load := 1 - idle/m.cpuStats.NumCPU()
	m.promCPULoad.Set(load)

	m.mu.Lock()
	defer m.mu.Unlock()

	maxUsage := 0.0
	var maxEgress string
	for pid, cpuUsage := range usage {
		procStats := m.procStats[pid]
		if procStats == nil {
			continue
		}

		procStats.lastUsage = cpuUsage
		procStats.totalCPU += cpuUsage
		procStats.cpuCounter++
		if cpuUsage > procStats.maxCPU {
			procStats.maxCPU = cpuUsage
		}

		if cpuUsage > procStats.allowedUsage && cpuUsage > maxUsage {
			maxUsage = cpuUsage
			maxEgress = procStats.egressID
		}
	}

	killThreshold := defaultKillThreshold
	if killThreshold <= m.cpuCostConfig.MaxCpuUtilization {
		killThreshold = (1 + m.cpuCostConfig.MaxCpuUtilization) / 2
	}

	if load > killThreshold {
		logger.Warnw("high cpu usage", nil,
			"load", load,
			"requests", m.requests.Load(),
		)

		if m.requests.Load() > 1 {
			m.highCPUDuration++
			if m.highCPUDuration < minKillDuration {
				return
			}
			m.killProcess(maxEgress, maxUsage)
		}
	}

	m.highCPUDuration = 0
}

func (m *Monitor) CanAcceptRequest(req *rpc.StartEgressRequest) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.canAcceptRequestLocked(req)
}

func (m *Monitor) canAcceptRequestLocked(req *rpc.StartEgressRequest) bool {
	accept := false
	total := m.cpuStats.NumCPU()

	var available, pending, used float64
	if m.requests.Load() == 0 {
		// if no requests, use total
		available = total
	} else {
		for _, ps := range m.pending {
			if ps.pendingUsage > ps.lastUsage {
				pending += ps.pendingUsage
			} else {
				pending += ps.lastUsage
			}
		}
		for _, ps := range m.procStats {
			if ps.pendingUsage > ps.lastUsage {
				used += ps.pendingUsage
			} else {
				used += ps.lastUsage
			}
		}

		// if already running requests, cap usage at MaxCpuUtilization
		available = total*m.cpuCostConfig.MaxCpuUtilization - pending - used
	}

	var required float64
	switch r := req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		if r.RoomComposite.AudioOnly {
			required = m.cpuCostConfig.AudioRoomCompositeCpuCost
		} else {
			required = m.cpuCostConfig.RoomCompositeCpuCost
		}
	case *rpc.StartEgressRequest_Web:
		if r.Web.AudioOnly {
			required = m.cpuCostConfig.AudioWebCpuCost
		} else {
			required = m.cpuCostConfig.WebCpuCost
		}
	case *rpc.StartEgressRequest_Participant:
		required = m.cpuCostConfig.ParticipantCpuCost
	case *rpc.StartEgressRequest_TrackComposite:
		required = m.cpuCostConfig.TrackCompositeCpuCost
	case *rpc.StartEgressRequest_Track:
		required = m.cpuCostConfig.TrackCpuCost
	}
	accept = available >= required

	logger.Debugw("cpu check",
		"total", total,
		"pending", pending,
		"used", used,
		"required", required,
		"available", available,
		"activeRequests", m.requests.Load(),
		"canAccept", accept,
	)

	return accept
}

func (m *Monitor) AcceptRequest(req *rpc.StartEgressRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.canAcceptRequestLocked(req) {
		return errors.ErrResourceExhausted
	}

	m.requests.Inc()

	var cpuHold float64
	switch r := req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		if r.RoomComposite.AudioOnly {
			cpuHold = m.cpuCostConfig.AudioRoomCompositeCpuCost
		} else {
			cpuHold = m.cpuCostConfig.RoomCompositeCpuCost
		}
	case *rpc.StartEgressRequest_Web:
		if r.Web.AudioOnly {
			cpuHold = m.cpuCostConfig.AudioWebCpuCost
		} else {
			cpuHold = m.cpuCostConfig.WebCpuCost
		}
	case *rpc.StartEgressRequest_Participant:
		cpuHold = m.cpuCostConfig.ParticipantCpuCost
	case *rpc.StartEgressRequest_TrackComposite:
		cpuHold = m.cpuCostConfig.TrackCompositeCpuCost
	case *rpc.StartEgressRequest_Track:
		cpuHold = m.cpuCostConfig.TrackCpuCost
	}

	ps := &processStats{
		egressID:     req.EgressId,
		pendingUsage: cpuHold,
		allowedUsage: cpuHold,
	}
	time.AfterFunc(cpuHoldDuration, func() { ps.pendingUsage = 0 })
	m.pending[req.EgressId] = ps

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

func (m *Monitor) EgressEnded(req *rpc.StartEgressRequest) (float64, float64) {
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

	delete(m.pending, req.EgressId)
	m.requests.Dec()

	for pid, ps := range m.procStats {
		if ps.egressID == req.EgressId {
			delete(m.procStats, pid)
			return ps.totalCPU / float64(ps.cpuCounter), ps.maxCPU
		}
	}

	return 0, 0
}

func (m *Monitor) EgressAborted(req *rpc.StartEgressRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.pending, req.EgressId)
	m.requests.Dec()
}
