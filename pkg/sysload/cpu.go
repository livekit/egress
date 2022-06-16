package sysload

import (
	"runtime"
	"sort"
	"time"

	"github.com/frostbyte73/go-throttle"
	"github.com/mackerelio/go-osstat/cpu"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/errors"
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

func Init(conf *config.Config, close chan struct{}, isAvailable func() float64) error {
	if err := checkCPUConfig(conf.CPUCost); err != nil {
		return err
	}

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
	return nil
}

func checkCPUConfig(costConfig config.CPUCostConfig) error {
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
		costConfig.TrackCpuCost,
		costConfig.TrackCompositeCpuCost,
		costConfig.RoomCompositeCpuCost,
	}
	sort.Float64s(requirements)

	recommendedMinimum := requirements[2]
	if recommendedMinimum < 3 {
		recommendedMinimum = 3
	}

	if numCPUs < requirements[0] {
		recommended := requirements[2]
		if recommended < 3 {
			recommended = 3
		}
		logger.Errorw("not enough cpu", nil,
			"minimum cpu", requirements[0],
			"recommended", recommended,
			"available", numCPUs,
		)
		return errors.New("not enough cpu")
	}

	if numCPUs < requirements[2] {
		logger.Errorw("not enough cpu for all request types", nil,
			"minimum cpu", requirements[2],
			"recommended", recommendedMinimum,
			"available", numCPUs,
		)
	}

	return nil
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
