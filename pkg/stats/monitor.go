package stats

import (
	"fmt"
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

type Monitor struct {
	cpuCostConfig config.CPUCostConfig

	promCPULoad  prometheus.Gauge
	requestGauge *prometheus.GaugeVec

	cpuStats *utils.CPUStats

	pendingCPUs atomic.Float64
}

func NewMonitor(conf *config.ServiceConfig) *Monitor {
	return &Monitor{
		cpuCostConfig: conf.CPUCostConfig,
	}
}

func (m *Monitor) Start(conf *config.ServiceConfig, isAvailable func() float64) error {
	cpuStats, err := utils.NewCPUStats(func(idle float64) {
		m.promCPULoad.Set(1 - idle/float64(m.cpuStats.NumCPU()))
	})
	if err != nil {
		return err
	}

	m.cpuStats = cpuStats

	if err := m.checkCPUConfig(); err != nil {
		return err
	}

	promNodeAvailable := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "available",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "cluster_id": conf.ClusterID},
	}, isAvailable)

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

	prometheus.MustRegister(promNodeAvailable, m.promCPULoad, m.requestGauge)

	return nil
}

func (m *Monitor) checkCPUConfig() error {
	if m.cpuCostConfig.RoomCompositeCpuCost < 2.5 {
		logger.Warnw("room composite requirement too low", nil,
			"config value", m.cpuCostConfig.RoomCompositeCpuCost,
			"minimum value", 2.5,
			"recommended value", 3,
		)
	}
	if m.cpuCostConfig.WebCpuCost < 2.5 {
		logger.Warnw("web requirement too low", nil,
			"config value", m.cpuCostConfig.WebCpuCost,
			"minimum value", 2.5,
			"recommended value", 3,
		)
	}
	if m.cpuCostConfig.TrackCompositeCpuCost < 1 {
		logger.Warnw("track composite requirement too low", nil,
			"config value", m.cpuCostConfig.TrackCompositeCpuCost,
			"minimum value", 1,
			"recommended value", 2,
		)
	}
	if m.cpuCostConfig.TrackCpuCost < 0.5 {
		logger.Warnw("track requirement too low", nil,
			"config value", m.cpuCostConfig.RoomCompositeCpuCost,
			"minimum value", 0.5,
			"recommended value", 1,
		)
	}

	requirements := []float64{
		m.cpuCostConfig.RoomCompositeCpuCost,
		m.cpuCostConfig.WebCpuCost,
		m.cpuCostConfig.TrackCompositeCpuCost,
		m.cpuCostConfig.TrackCpuCost,
	}
	sort.Float64s(requirements)

	recommendedMinimum := requirements[len(requirements)-1]
	if recommendedMinimum < 3 {
		recommendedMinimum = 3
	}

	if float64(m.cpuStats.NumCPU()) < requirements[0] {
		logger.Errorw("not enough cpu", nil,
			"minimum cpu", requirements[0],
			"recommended", recommendedMinimum,
			"available", m.cpuStats.NumCPU(),
		)
		return errors.New("not enough cpu")
	}

	if float64(m.cpuStats.NumCPU()) < requirements[len(requirements)-1] {
		logger.Errorw("not enough cpu for some egress types", nil,
			"minimum cpu", requirements[len(requirements)-1],
			"recommended", recommendedMinimum,
			"available", m.cpuStats.NumCPU(),
		)
	}

	logger.Infow(fmt.Sprintf("available CPU cores: %d max cost: %f", m.cpuStats.NumCPU(), requirements[len(requirements)-1]))

	return nil
}

func (m *Monitor) GetCPULoad() float64 {
	return (float64(m.cpuStats.NumCPU()) - m.cpuStats.GetCPUIdle()) / float64(m.cpuStats.NumCPU()) * 100
}

func (m *Monitor) CanAcceptRequest(req *rpc.StartEgressRequest) bool {
	accept := false
	available := m.cpuStats.GetCPUIdle() - m.pendingCPUs.Load()

	switch req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		accept = available > m.cpuCostConfig.RoomCompositeCpuCost
	case *rpc.StartEgressRequest_Web:
		accept = available > m.cpuCostConfig.WebCpuCost
	case *rpc.StartEgressRequest_TrackComposite:
		accept = available > m.cpuCostConfig.TrackCompositeCpuCost
	case *rpc.StartEgressRequest_Track:
		accept = available > m.cpuCostConfig.TrackCpuCost
	}

	return accept
}

func (m *Monitor) AcceptRequest(req *rpc.StartEgressRequest) {
	var cpuHold float64
	switch req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		cpuHold = m.cpuCostConfig.RoomCompositeCpuCost
	case *rpc.StartEgressRequest_Web:
		cpuHold = m.cpuCostConfig.WebCpuCost
	case *rpc.StartEgressRequest_TrackComposite:
		cpuHold = m.cpuCostConfig.TrackCompositeCpuCost
	case *rpc.StartEgressRequest_Track:
		cpuHold = m.cpuCostConfig.TrackCpuCost
	}

	m.pendingCPUs.Add(cpuHold)
	time.AfterFunc(time.Second, func() { m.pendingCPUs.Sub(cpuHold) })
}

func (m *Monitor) EgressStarted(req *rpc.StartEgressRequest) {
	switch req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		m.requestGauge.With(prometheus.Labels{"type": "room_composite"}).Add(1)
	case *rpc.StartEgressRequest_Web:
		m.requestGauge.With(prometheus.Labels{"type": "web"}).Add(1)
	case *rpc.StartEgressRequest_TrackComposite:
		m.requestGauge.With(prometheus.Labels{"type": "track_composite"}).Add(1)
	case *rpc.StartEgressRequest_Track:
		m.requestGauge.With(prometheus.Labels{"type": "track"}).Add(1)
	}
}

func (m *Monitor) EgressEnded(req *rpc.StartEgressRequest) {
	switch req.Request.(type) {
	case *rpc.StartEgressRequest_RoomComposite:
		m.requestGauge.With(prometheus.Labels{"type": "room_composite"}).Sub(1)
	case *rpc.StartEgressRequest_Web:
		m.requestGauge.With(prometheus.Labels{"type": "web"}).Sub(1)
	case *rpc.StartEgressRequest_TrackComposite:
		m.requestGauge.With(prometheus.Labels{"type": "track_composite"}).Sub(1)
	case *rpc.StartEgressRequest_Track:
		m.requestGauge.With(prometheus.Labels{"type": "track"}).Sub(1)
	}
}
