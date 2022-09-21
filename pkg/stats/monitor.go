package stats

import (
	"runtime"
	"sort"
	"time"

	"github.com/frostbyte73/go-throttle"
	"github.com/mackerelio/go-osstat/cpu"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type Monitor struct {
	cpuCostConfig config.CPUCostConfig

	promCPULoad  prometheus.Gauge
	requestGauge *prometheus.GaugeVec

	idleCPUs        atomic.Float64
	pendingCPUs     atomic.Float64
	numCPUs         float64
	warningThrottle func(func())
}

func NewMonitor() *Monitor {
	return &Monitor{
		numCPUs:         float64(runtime.NumCPU()),
		warningThrottle: throttle.New(time.Minute),
	}
}

func (m *Monitor) Start(conf *config.Config, close chan struct{}, isAvailable func() float64) error {
	if err := m.checkCPUConfig(conf.CPUCost); err != nil {
		return err
	}

	promNodeAvailable := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "available",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID},
	}, isAvailable)

	m.promCPULoad = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "node",
		Name:        "cpu_load",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "node_type": "EGRESS"},
	})

	m.requestGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "requests",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID},
	}, []string{"type"})

	prometheus.MustRegister(promNodeAvailable, m.promCPULoad, m.requestGauge)

	go m.monitorCPULoad(close)
	return nil
}

func (m *Monitor) checkCPUConfig(costConfig config.CPUCostConfig) error {
	if costConfig.RoomCompositeCpuCost < 2.5 {
		logger.Warnw("room composite requirement too low", nil,
			"config value", costConfig.RoomCompositeCpuCost,
			"minimum value", 2.5,
			"recommended value", 3,
		)
	}
	if costConfig.TrackCompositeCpuCost < 1 {
		logger.Warnw("track composite requirement too low", nil,
			"config value", costConfig.TrackCompositeCpuCost,
			"minimum value", 1,
			"recommended value", 2,
		)
	}
	if costConfig.TrackCpuCost < 0.5 {
		logger.Warnw("track requirement too low", nil,
			"config value", costConfig.RoomCompositeCpuCost,
			"minimum value", 0.5,
			"recommended value", 1,
		)
	}

	requirements := []float64{
		costConfig.RoomCompositeCpuCost,
		costConfig.TrackCompositeCpuCost,
		costConfig.TrackCpuCost,
	}
	sort.Float64s(requirements)

	recommendedMinimum := requirements[2]
	if recommendedMinimum < 3 {
		recommendedMinimum = 3
	}

	if m.numCPUs < requirements[0] {
		logger.Errorw("not enough cpu", nil,
			"minimum cpu", requirements[0],
			"recommended", recommendedMinimum,
			"available", m.numCPUs,
		)
		return errors.New("not enough cpu")
	}

	if m.numCPUs < requirements[2] {
		logger.Errorw("not enough cpu for some egress types", nil,
			"minimum cpu", requirements[2],
			"recommended", recommendedMinimum,
			"available", m.numCPUs,
		)
	}

	return nil
}

func (m *Monitor) monitorCPULoad(close chan struct{}) {
	prev, _ := cpu.Get()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-close:
			return
		case <-ticker.C:
			next, _ := cpu.Get()
			idlePercent := float64(next.Idle-prev.Idle) / float64(next.Total-prev.Total)
			m.idleCPUs.Store(m.numCPUs * idlePercent)
			m.promCPULoad.Set(1 - idlePercent)

			if idlePercent < 0.1 {
				m.warningThrottle(func() { logger.Infow("high cpu load", "load", 100-idlePercent) })
			}

			prev = next
		}
	}
}

func (m *Monitor) GetCPULoad() float64 {
	return (m.numCPUs - m.idleCPUs.Load()) / m.numCPUs * 100
}

func (m *Monitor) CanAcceptRequest(req *livekit.StartEgressRequest) bool {
	accept := false
	available := m.idleCPUs.Load() - m.pendingCPUs.Load()

	switch req.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		accept = available > m.cpuCostConfig.RoomCompositeCpuCost
	case *livekit.StartEgressRequest_TrackComposite:
		accept = available > m.cpuCostConfig.TrackCompositeCpuCost
	case *livekit.StartEgressRequest_Track:
		accept = available > m.cpuCostConfig.TrackCpuCost
	}

	logger.Debugw("cpu request", "accepted", accept, "availableCPUs", available, "numCPUs", runtime.NumCPU())
	return accept
}

func (m *Monitor) AcceptRequest(req *livekit.StartEgressRequest) {
	var cpuHold float64
	switch req.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		cpuHold = m.cpuCostConfig.RoomCompositeCpuCost
	case *livekit.StartEgressRequest_TrackComposite:
		cpuHold = m.cpuCostConfig.TrackCompositeCpuCost
	case *livekit.StartEgressRequest_Track:
		cpuHold = m.cpuCostConfig.TrackCpuCost
	}

	m.pendingCPUs.Add(cpuHold)
	time.AfterFunc(time.Second, func() { m.pendingCPUs.Sub(cpuHold) })
}

func (m *Monitor) EgressStarted(req *livekit.StartEgressRequest) {
	switch req.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		m.requestGauge.With(prometheus.Labels{"type": "room_composite"}).Add(1)
	case *livekit.StartEgressRequest_TrackComposite:
		m.requestGauge.With(prometheus.Labels{"type": "track_composite"}).Add(1)
	case *livekit.StartEgressRequest_Track:
		m.requestGauge.With(prometheus.Labels{"type": "track"}).Add(1)
	}
}

func (m *Monitor) EgressEnded(req *livekit.StartEgressRequest) {
	switch req.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		m.requestGauge.With(prometheus.Labels{"type": "room_composite"}).Sub(1)
	case *livekit.StartEgressRequest_TrackComposite:
		m.requestGauge.With(prometheus.Labels{"type": "track_composite"}).Sub(1)
	case *livekit.StartEgressRequest_Track:
		m.requestGauge.With(prometheus.Labels{"type": "track"}).Sub(1)
	}
}
