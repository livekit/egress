package sysload

import (
	"runtime"
	"time"

	"github.com/frostbyte73/go-throttle"
	"github.com/mackerelio/go-osstat/cpu"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/pkg/config"
)

var (
	idleCPUs        atomic.Float64
	pendingCPUs     atomic.Float64
	numCPUs         = float64(runtime.NumCPU())
	warningThrottle = throttle.New(time.Minute)

	promCPULoad   prometheus.Gauge
	cpuCostConfig config.CPUCostConfig
)

func Init(conf *config.Config, close chan struct{}, isAvailable func() float64) {
	cpuCostConfig = conf.CPUCost

	promCPULoad = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "node",
		Name:        "cpu_load",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "node_type": "EGRESS"},
	})
	promNodeAvailable := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "available",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID},
	}, isAvailable)

	prometheus.MustRegister(promCPULoad)
	prometheus.MustRegister(promNodeAvailable)

	go monitorCPULoad(close)
}

func monitorCPULoad(close chan struct{}) {
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
			idleCPUs.Store(numCPUs * idlePercent)
			promCPULoad.Set(1 - idlePercent)

			if idlePercent < 0.1 {
				warningThrottle(func() { logger.Infow("high cpu load", "load", 100-idlePercent) })
			}

			prev = next
		}
	}
}

func GetCPULoad() float64 {
	return (numCPUs - idleCPUs.Load()) / numCPUs * 100
}

func CanAcceptRequest(req *livekit.StartEgressRequest) bool {
	accept := false
	available := idleCPUs.Load() - pendingCPUs.Load()

	switch req.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		accept = available > cpuCostConfig.RoomCompositeCpuCost
	case *livekit.StartEgressRequest_TrackComposite:
		accept = available > cpuCostConfig.TrackCompositeCpuCost
	case *livekit.StartEgressRequest_Track:
		accept = available > cpuCostConfig.TrackCpuCost
	}

	logger.Debugw("cpu request", "accepted", accept, "availableCPUs", available, "numCPUs", runtime.NumCPU())
	return accept
}

func AcceptRequest(req *livekit.StartEgressRequest) {
	var cpuHold float64
	switch req.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		cpuHold = cpuCostConfig.RoomCompositeCpuCost
	case *livekit.StartEgressRequest_TrackComposite:
		cpuHold = cpuCostConfig.TrackCompositeCpuCost
	case *livekit.StartEgressRequest_Track:
		cpuHold = cpuCostConfig.TrackCpuCost
	}

	pendingCPUs.Add(cpuHold)
	time.AfterFunc(time.Second, func() { pendingCPUs.Sub(cpuHold) })
}
