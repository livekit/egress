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

	"github.com/livekit/egress/pkg/cgroupmem"
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

	promCPULoad                prometheus.Gauge
	promCgroupMemory           prometheus.Gauge
	promCgroupInactiveFile     prometheus.Gauge
	promCgroupWorkingSet       prometheus.Gauge
	promCgroupReadSuccess      prometheus.Gauge
	promProcRSS                prometheus.Gauge
	promWouldRejectCgroupTotal prometheus.Gauge
	promWouldRejectCgroupWS    prometheus.Gauge
	requestGauge               *prometheus.GaugeVec

	svc                 Service
	cpuStats            *hwstats.CPUStats
	cgroupReader        *cgroupmem.Reader
	requests            atomic.Int32
	webRequests         atomic.Int32
	pendingPulseClients atomic.Int32
	pendingMemoryUsage  atomic.Float64

	mu                    deadlock.Mutex
	highCPUDuration       int
	highMemoryDuration    int
	pending               map[string]*processStats
	procStats             map[int]*processStats
	memoryUsage           float64
	cgroupTotalBytes      uint64
	cgroupWorkingSetBytes uint64
	cgroupOK              bool
	cgroupErrorLogged     atomic.Bool
}

type processStats struct {
	egressID string

	pendingCPU float64
	lastCPU    float64
	allowedCPU float64

	totalCPU   float64
	cpuCounter int
	maxCPU     float64
	maxMemory  int
}

func NewMonitor(conf *config.ServiceConfig, svc Service) (*Monitor, error) {
	m := &Monitor{
		nodeID:        conf.NodeID,
		clusterID:     conf.ClusterID,
		cpuCostConfig: conf.CPUCostConfig,
		svc:           svc,
		cgroupReader:  cgroupmem.NewReader(),
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

	// Log memory configuration at startup
	logger.Infow("memory monitoring configured",
		"memorySource", conf.CPUCostConfig.MemorySource,
		"maxMemoryGB", conf.CPUCostConfig.MaxMemory,
		"memoryHeadroomGB", *conf.CPUCostConfig.MemoryHeadroomGB,
		"memoryHardHeadroomGB", *conf.CPUCostConfig.MemoryHardHeadroomGB,
		"memoryKillGraceSec", conf.CPUCostConfig.MemoryKillGraceSec,
	)

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

func (m *Monitor) CanAcceptRequest(req *rpc.StartEgressRequest) bool {
	m.mu.Lock()
	fields, canAccept := m.canAcceptRequestLocked(req)
	m.mu.Unlock()

	logger.Debugw("cpu check", fields...)
	return canAccept
}

func (m *Monitor) CanAcceptWebRequest() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.canAcceptWebLocked()
}

func (m *Monitor) canAcceptRequestLocked(req *rpc.StartEgressRequest) ([]interface{}, bool) {
	total, available, pending, used := m.getCPUUsageLocked()
	fields := []interface{}{
		"total", total,
		"available", available,
		"pending", pending,
		"used", used,
		"activeRequests", m.requests.Load(),
		"activeWeb", m.webRequests.Load(),
		"memory", m.memoryUsage,
		"memorySource", m.cpuCostConfig.MemorySource,
	}

	// Memory admission check based on configured source
	if reject, reason := m.checkMemoryAdmissionLocked(); reject {
		fields = append(fields, "canAccept", false, "reason", reason)
		return fields, false
	}

	required := req.EstimatedCpu
	switch r := req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		if !m.canAcceptWebLocked() {
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

// checkMemoryAdmissionLocked checks if a request should be rejected due to memory constraints.
// Returns (reject, reason) where reject=true means the request should be rejected.
func (m *Monitor) checkMemoryAdmissionLocked() (bool, string) {
	if m.cpuCostConfig.MaxMemory == 0 {
		return false, ""
	}

	pendingMem := m.pendingMemoryUsage.Load()
	memoryCost := m.cpuCostConfig.MemoryCost
	headroom := *m.cpuCostConfig.MemoryHeadroomGB
	maxMem := m.cpuCostConfig.MaxMemory

	switch m.cpuCostConfig.MemorySource {
	case config.MemorySourceCgroupTotal:
		if !m.cgroupOK {
			// Fallback to proc_rss
			return m.checkProcRSSMemoryAdmission(pendingMem, memoryCost, headroom, maxMem)
		}
		cgroupGB := float64(m.cgroupTotalBytes) / gb
		if cgroupGB+pendingMem+memoryCost+headroom >= maxMem {
			return true, "memory_cgroup_total"
		}

	case config.MemorySourceCgroupWorkingSet:
		if !m.cgroupOK {
			return m.checkProcRSSMemoryAdmission(pendingMem, memoryCost, headroom, maxMem)
		}
		wsGB := float64(m.cgroupWorkingSetBytes) / gb
		if wsGB+pendingMem+memoryCost+headroom >= maxMem {
			return true, "memory_cgroup_workingset"
		}

	case config.MemorySourceHybrid:
		if !m.cgroupOK {
			return m.checkProcRSSMemoryAdmission(pendingMem, memoryCost, headroom, maxMem)
		}
		// Soft gate on working set
		wsGB := float64(m.cgroupWorkingSetBytes) / gb
		if wsGB+pendingMem+memoryCost+headroom >= maxMem {
			return true, "memory_cgroup_workingset_soft"
		}
		// Hard gate on total (with extra headroom)
		hardHeadroom := headroom + *m.cpuCostConfig.MemoryHardHeadroomGB
		totalGB := float64(m.cgroupTotalBytes) / gb
		if totalGB+pendingMem+memoryCost+hardHeadroom >= maxMem {
			return true, "memory_cgroup_total_hard"
		}

	default: // proc_rss
		return m.checkProcRSSMemoryAdmission(pendingMem, memoryCost, headroom, maxMem)
	}

	return false, ""
}

// checkProcRSSMemoryAdmission implements the original per-process RSS based admission.
func (m *Monitor) checkProcRSSMemoryAdmission(pendingMem, memoryCost, headroom, maxMem float64) (bool, string) {
	memoryUsage := m.memoryUsage + pendingMem
	if memoryUsage+memoryCost+headroom >= maxMem {
		return true, "memory"
	}
	return false, ""
}

func (m *Monitor) AcceptRequest(req *rpc.StartEgressRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.pending[req.EgressId] != nil {
		return errors.ErrEgressAlreadyExists
	}
	if _, ok := m.canAcceptRequestLocked(req); !ok {
		logger.Warnw("can not accept request", nil)
		return errors.ErrNotEnoughCPU
	}

	m.requests.Inc()
	var cpuHold float64
	var pulseClients int32
	switch r := req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		m.webRequests.Inc()
		pulseClients = pulseClientHold
		if r.RoomComposite.AudioOnly {
			cpuHold = m.cpuCostConfig.AudioRoomCompositeCpuCost
		} else {
			cpuHold = m.cpuCostConfig.RoomCompositeCpuCost
		}
	case *rpc.StartEgressRequest_Web:
		m.webRequests.Inc()
		pulseClients = pulseClientHold
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
		egressID:   req.EgressId,
		pendingCPU: cpuHold,
		allowedCPU: cpuHold,
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

	delete(m.pending, req.EgressId)
	m.requests.Dec()
	switch req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite, *rpc.StartEgressRequest_Web:
		m.webRequests.Dec()
	}
}

func (m *Monitor) EgressEnded(req *rpc.StartEgressRequest) (float64, float64, int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		m.requestGauge.With(prometheus.Labels{"type": types.RequestTypeRoomComposite}).Sub(1)
		m.webRequests.Dec()
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

	// Collect proc RSS per-process memory stats
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
	m.promProcRSS.Set(float64(totalMemory))

	// Read cgroup memory stats (always, for metrics)
	m.updateCgroupStats()

	// Update "would reject" metrics for evaluation purposes
	m.updateWouldRejectMetrics()

	// Memory kill logic based on configured source
	m.checkMemoryKill(maxMemoryEgress)
}

// updateCgroupStats reads cgroup memory statistics and updates metrics.
func (m *Monitor) updateCgroupStats() {
	cgStats, err := m.cgroupReader.Read()
	if err != nil {
		m.cgroupOK = false
		m.promCgroupReadSuccess.Set(0)
		// Throttle error logging (CompareAndSwap ensures we log only once)
		if m.cgroupErrorLogged.CompareAndSwap(false, true) {
			logger.Warnw("failed to read cgroup memory stats, falling back to proc_rss", err)
		}
		return
	}

	m.cgroupOK = true
	m.cgroupErrorLogged.Store(false)
	m.cgroupTotalBytes = cgStats.TotalBytes
	m.cgroupWorkingSetBytes = cgStats.WorkingSetBytes

	m.promCgroupReadSuccess.Set(1)
	m.promCgroupMemory.Set(float64(cgStats.TotalBytes))
	m.promCgroupInactiveFile.Set(float64(cgStats.InactiveFileBytes))
	m.promCgroupWorkingSet.Set(float64(cgStats.WorkingSetBytes))
	logger.Infow("cgroup memory stats", "version", cgStats.Version, "totalBytes", cgStats.TotalBytes, "inactiveFileBytes", cgStats.InactiveFileBytes, "workingSetBytes", cgStats.WorkingSetBytes)
	logger.Infow("rss memory stats", "memoryUsage", m.memoryUsage)
}

// updateWouldRejectMetrics computes what admission would do with alternative memory sources.
func (m *Monitor) updateWouldRejectMetrics() {
	if !m.cgroupOK || m.cpuCostConfig.MaxMemory == 0 {
		return
	}

	pendingMem := m.pendingMemoryUsage.Load()
	headroom := *m.cpuCostConfig.MemoryHeadroomGB
	maxMem := m.cpuCostConfig.MaxMemory

	// Would reject with cgroup_total?
	cgroupTotalGB := float64(m.cgroupTotalBytes) / gb
	if cgroupTotalGB+pendingMem+m.cpuCostConfig.MemoryCost+headroom >= maxMem {
		m.promWouldRejectCgroupTotal.Set(1)
	} else {
		m.promWouldRejectCgroupTotal.Set(0)
	}

	// Would reject with cgroup_workingset?
	cgroupWSGB := float64(m.cgroupWorkingSetBytes) / gb
	if cgroupWSGB+pendingMem+m.cpuCostConfig.MemoryCost+headroom >= maxMem {
		m.promWouldRejectCgroupWS.Set(1)
	} else {
		m.promWouldRejectCgroupWS.Set(0)
	}
}

// checkMemoryKill evaluates whether to kill a process based on memory usage.
func (m *Monitor) checkMemoryKill(maxMemoryEgress string) {
	if m.cpuCostConfig.MaxMemory == 0 {
		return
	}

	maxMemoryBytes := uint64(m.cpuCostConfig.MaxMemory * gb)
	var killTriggerBytes uint64

	switch m.cpuCostConfig.MemorySource {
	case config.MemorySourceCgroupTotal:
		if !m.cgroupOK {
			// Fallback to proc_rss
			killTriggerBytes = uint64(m.memoryUsage * gb)
		} else {
			killTriggerBytes = m.cgroupTotalBytes
		}
	case config.MemorySourceCgroupWorkingSet:
		// For working set mode, still kill based on total (safer)
		if !m.cgroupOK {
			killTriggerBytes = uint64(m.memoryUsage * gb)
		} else {
			killTriggerBytes = m.cgroupTotalBytes
		}
	case config.MemorySourceHybrid:
		// Kill on total
		if !m.cgroupOK {
			killTriggerBytes = uint64(m.memoryUsage * gb)
		} else {
			killTriggerBytes = m.cgroupTotalBytes
		}
	default: // proc_rss
		killTriggerBytes = uint64(m.memoryUsage * gb)
	}

	if killTriggerBytes > maxMemoryBytes {
		// Apply grace period if configured.
		// Note: highMemoryDuration counts update cycles (typically 1 second each),
		// so MemoryKillGraceSec is approximate.
		m.highMemoryDuration++
		if m.highMemoryDuration > m.cpuCostConfig.MemoryKillGraceSec {
			killTriggerGB := float64(killTriggerBytes) / gb
			logger.Warnw("high memory usage", nil,
				"source", m.cpuCostConfig.MemorySource,
				"memoryGB", killTriggerGB,
				"maxMemoryGB", m.cpuCostConfig.MaxMemory,
				"requests", m.requests.Load(),
			)
			// Report the actual memory that triggered the kill, not per-process max
			m.svc.KillProcess(maxMemoryEgress, errors.ErrOOM(killTriggerGB))
			m.highMemoryDuration = 0
		}
	} else {
		m.highMemoryDuration = 0
	}
}
