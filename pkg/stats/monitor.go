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
	"time"

	"github.com/linkdata/deadlock"
	"github.com/pbnjay/memory"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/source/pulse"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils/hwstats"
)

const (
	cpuHoldDuration      = time.Second * 15
	defaultKillThreshold = 0.95
	minKillDuration      = 10
	gb                   = 1024.0 * 1024.0 * 1024.0
	pulseClientHold      = 4
	memoryBuffer         = 1
)

type Service interface {
	IsIdle() bool
	IsDisabled() bool
	IsTerminating() bool
	KillProcess(string, error)
}

type Monitor struct {
	nodeID        string
	clusterID     string
	cpuCostConfig *config.CPUCostConfig

	promCPULoad  prometheus.Gauge
	requestGauge *prometheus.GaugeVec

	svc                 Service
	cpuStats            *hwstats.CPUStats
	requests            atomic.Int32
	webRequests         atomic.Int32
	pendingPulseClients atomic.Int32
	pendingMemoryUsage  atomic.Float64

	mu              deadlock.Mutex
	highCPUDuration int
	pending         map[string]*processStats
	procStats       map[int]*processStats
	memoryUsage     float64
}

type processStats struct {
	egressID string

	pendingCPU float64
	lastCPU    float64
	allowedCPU float64

	totalCPU     float64
	cpuCounter   int
	maxCPU       float64
	maxMemory    int
	countedAsWeb bool
}

func NewMonitor(conf *config.ServiceConfig, svc Service) (*Monitor, error) {
	m := &Monitor{
		nodeID:        conf.NodeID,
		clusterID:     conf.ClusterID,
		cpuCostConfig: conf.CPUCostConfig,
		svc:           svc,
		pending:       make(map[string]*processStats),
		procStats:     make(map[int]*processStats),
	}

	m.initPrometheus()

	procStats, err := hwstats.NewProcMonitor(m.updateEgressStats)
	if err != nil {
		return nil, err
	}
	m.cpuStats = procStats

	if err = m.validateCPUConfig(); err != nil {
		return nil, err
	}

	return m, nil
}

func (m *Monitor) validateCPUConfig() error {
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

func (m *Monitor) CanAcceptRequest(req *rpc.StartEgressRequest, sdkRoomCompositeEnabled bool) bool {
	m.mu.Lock()
	fields, canAccept := m.canAcceptRequestLocked(req, sdkRoomCompositeEnabled)
	m.mu.Unlock()

	logger.Debugw("cpu check", fields...)
	return canAccept
}

func (m *Monitor) CanAcceptWebRequest() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.canAcceptWebLocked()
}

func (m *Monitor) canAcceptRequestLocked(req *rpc.StartEgressRequest, sdkRoomCompositeEnabled bool) ([]interface{}, bool) {
	total, available, pending, used := m.getCPUUsageLocked()
	fields := []interface{}{
		"total", total,
		"available", available,
		"pending", pending,
		"used", used,
		"activeRequests", m.requests.Load(),
		"activeWeb", m.webRequests.Load(),
		"memory", m.memoryUsage,
	}

	memoryUsage := m.memoryUsage + m.pendingMemoryUsage.Load()

	if m.cpuCostConfig.MaxMemory > 0 && memoryUsage+m.cpuCostConfig.MemoryCost+memoryBuffer >= m.cpuCostConfig.MaxMemory {
		fields = append(fields, "canAccept", false, "reason", "memory")
		return fields, false
	}

	required := req.EstimatedCpu
	switch r := req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		useSDK := config.RoomCompositeUsesSDKSource(r.RoomComposite, sdkRoomCompositeEnabled)
		if !useSDK && !m.canAcceptWebLocked() {
			fields = append(fields, "canAccept", false, "reason", "pulse clients")
			return fields, false
		}
		if required == 0 {
			if r.RoomComposite.AudioOnly {
				required = m.cpuCostConfig.AudioRoomCompositeCpuCost
			} else {
				required = m.cpuCostConfig.RoomCompositeCpuCost
			}
		}
	case *rpc.StartEgressRequest_Web:
		if !m.canAcceptWebLocked() {
			fields = append(fields, "canAccept", false, "reason", "pulse clients")
			return fields, false
		}
		if required == 0 {
			if r.Web.AudioOnly {
				required = m.cpuCostConfig.AudioWebCpuCost
			} else {
				required = m.cpuCostConfig.WebCpuCost
			}
		}
	case *rpc.StartEgressRequest_Participant:
		if required == 0 {
			required = m.cpuCostConfig.ParticipantCpuCost
		}
	case *rpc.StartEgressRequest_TrackComposite:
		if required == 0 {
			required = m.cpuCostConfig.TrackCompositeCpuCost
		}
	case *rpc.StartEgressRequest_Track:
		if required == 0 {
			required = m.cpuCostConfig.TrackCpuCost
		}
	}

	accept := available >= required
	fields = append(fields,
		"required", required,
		"canAccept", accept,
	)
	if !accept {
		fields = append(fields, "reason", "cpu")
	}

	return fields, accept
}

func (m *Monitor) canAcceptWebLocked() bool {
	clients, err := pulse.Clients()
	if err != nil {
		return false
	}
	return clients+int(m.pendingPulseClients.Load())+pulseClientHold <= m.cpuCostConfig.MaxPulseClients
}

func (m *Monitor) AcceptRequest(req *rpc.StartEgressRequest, sdkRoomCompositeEnabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.pending[req.EgressId] != nil {
		return errors.ErrEgressAlreadyExists
	}
	if _, ok := m.canAcceptRequestLocked(req, sdkRoomCompositeEnabled); !ok {
		logger.Warnw("can not accept request", nil)
		return errors.ErrNotEnoughCPU
	}

	m.requests.Inc()
	var cpuHold float64
	var pulseClients int32
	var countedAsWeb bool

	switch r := req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		useSDK := config.RoomCompositeUsesSDKSource(r.RoomComposite, sdkRoomCompositeEnabled)
		if !useSDK {
			m.webRequests.Inc()
			countedAsWeb = true
			pulseClients = pulseClientHold
		}
		if r.RoomComposite.AudioOnly {
			cpuHold = m.cpuCostConfig.AudioRoomCompositeCpuCost
		} else {
			cpuHold = m.cpuCostConfig.RoomCompositeCpuCost
		}
	case *rpc.StartEgressRequest_Web:
		pulseClients = pulseClientHold
		m.webRequests.Inc()
		countedAsWeb = true
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
		pendingCPU:   cpuHold,
		allowedCPU:   cpuHold,
		countedAsWeb: countedAsWeb,
	}

	m.pendingMemoryUsage.Add(m.cpuCostConfig.MemoryCost)
	m.pendingPulseClients.Add(pulseClients)

	time.AfterFunc(cpuHoldDuration, func() {
		ps.pendingCPU = 0
		m.pendingMemoryUsage.Add(-m.cpuCostConfig.MemoryCost)
		m.pendingPulseClients.Add(-pulseClients)
	})
	m.pending[req.EgressId] = ps

	return nil
}

func (m *Monitor) UpdatePID(egressID string, pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ps := m.pending[egressID]
	delete(m.pending, egressID)

	if ps == nil {
		logger.Warnw("missing pending procStats", nil, "egressID", egressID)
		ps = &processStats{
			egressID:   egressID,
			allowedCPU: m.cpuCostConfig.WebCpuCost,
		}
	}

	if existing := m.procStats[pid]; existing != nil {
		ps.maxCPU = existing.maxCPU
		ps.totalCPU = existing.totalCPU
		ps.cpuCounter = existing.cpuCounter
		ps.countedAsWeb = existing.countedAsWeb
	}
	m.procStats[pid] = ps
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

func (m *Monitor) EgressAborted(req *rpc.StartEgressRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ps := m.pending[req.EgressId]
	delete(m.pending, req.EgressId)
	m.requests.Dec()
	switch req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite, *rpc.StartEgressRequest_Web:
		if ps != nil && ps.countedAsWeb {
			m.webRequests.Dec()
		}
	}
}

func (m *Monitor) EgressEnded(req *rpc.StartEgressRequest) (float64, float64, int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var countedAsWeb bool
	if ps := m.pending[req.EgressId]; ps != nil {
		countedAsWeb = ps.countedAsWeb
	} else {
		for _, s := range m.procStats {
			if s.egressID == req.EgressId {
				countedAsWeb = s.countedAsWeb
				break
			}
		}
	}

	switch req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		m.requestGauge.With(prometheus.Labels{"type": types.RequestTypeRoomComposite}).Sub(1)
		if countedAsWeb {
			m.webRequests.Dec()
		}
	case *rpc.StartEgressRequest_Web:
		m.requestGauge.With(prometheus.Labels{"type": types.RequestTypeWeb}).Sub(1)
		m.webRequests.Dec()
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
			return ps.totalCPU / float64(ps.cpuCounter), ps.maxCPU, ps.maxMemory
		}
	}

	return 0, 0, 0
}

func (m *Monitor) GetAvailableCPU() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, available, _, _ := m.getCPUUsageLocked()
	return available
}

func (m *Monitor) getCPUUsageLocked() (total, available, pending, used float64) {
	total = m.cpuStats.NumCPU()
	if m.requests.Load() == 0 {
		// if no requests, use total
		available = total
		return
	}

	for _, ps := range m.pending {
		if ps.pendingCPU > ps.lastCPU {
			pending += ps.pendingCPU
		} else {
			pending += ps.lastCPU
		}
	}
	for _, ps := range m.procStats {
		if ps.pendingCPU > ps.lastCPU {
			used += ps.pendingCPU
		} else {
			used += ps.lastCPU
		}
	}

	// if already running requests, cap usage at MaxCpuUtilization
	available = total*m.cpuCostConfig.MaxCpuUtilization - pending - used
	return
}

func (m *Monitor) GetAvailableMemory() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cpuCostConfig.MaxMemory == 0 {
		return float64(memory.FreeMemory()) / gb
	}

	return m.cpuCostConfig.MaxMemory - m.memoryUsage
}

func (m *Monitor) updateEgressStats(stats *hwstats.ProcStats) {
	load := 1 - stats.CpuIdle/m.cpuStats.NumCPU()
	m.promCPULoad.Set(load)

	m.mu.Lock()
	defer m.mu.Unlock()

	maxCPU := 0.0
	var maxCPUEgress string
	for pid, cpuUsage := range stats.Cpu {
		procStats := m.procStats[pid]
		if procStats == nil {
			continue
		}

		procStats.lastCPU = cpuUsage
		procStats.totalCPU += cpuUsage
		procStats.cpuCounter++
		if cpuUsage > procStats.maxCPU {
			procStats.maxCPU = cpuUsage
		}

		if cpuUsage > procStats.allowedCPU && cpuUsage > maxCPU {
			maxCPU = cpuUsage
			maxCPUEgress = procStats.egressID
		}
	}

	cpuKillThreshold := defaultKillThreshold
	if cpuKillThreshold <= m.cpuCostConfig.MaxCpuUtilization {
		cpuKillThreshold = (1 + m.cpuCostConfig.MaxCpuUtilization) / 2
	}

	if load > cpuKillThreshold {
		logger.Warnw("high cpu usage", nil,
			"cpu", load,
			"requests", m.requests.Load(),
		)

		if m.requests.Load() > 1 {
			m.highCPUDuration++
			if m.highCPUDuration >= minKillDuration {
				m.svc.KillProcess(maxCPUEgress, errors.ErrCPUExhausted(maxCPU))
				m.highCPUDuration = 0
			}
		}
	}

	totalMemory := 0
	maxMemory := 0
	var maxMemoryEgress string
	for pid, memUsage := range stats.Memory {
		totalMemory += memUsage

		procStats := m.procStats[pid]
		if procStats == nil {
			continue
		}

		if memUsage > procStats.maxMemory {
			procStats.maxMemory = memUsage
		}
		if memUsage > maxMemory {
			maxMemory = memUsage
			maxMemoryEgress = procStats.egressID
		}
	}

	m.memoryUsage = float64(totalMemory) / gb
	if m.cpuCostConfig.MaxMemory > 0 && totalMemory > int(m.cpuCostConfig.MaxMemory*gb) {
		logger.Warnw("high memory usage", nil,
			"memory", m.memoryUsage,
			"requests", m.requests.Load(),
		)
		m.svc.KillProcess(maxMemoryEgress, errors.ErrOOM(float64(maxMemory)/gb))
	}
}
