package load

import (
	"fmt"
	"runtime"
	"time"

	"github.com/mackerelio/go-osstat/cpu"

	"github.com/livekit/protocol/logger"
)

func MonitorCPULoad(egressID string, done chan struct{}) {
	prev, _ := cpu.Get()
	var count, userTotal, userMax, systemTotal, systemMax, idleTotal float64
	idleMin := 100.0
	for {
		time.Sleep(time.Second)
		select {
		case <-done:
			avg := 100 - idleTotal/count
			max := 100 - idleMin
			numCPUs := runtime.NumCPU()
			logger.Infow("CPU load",
				"egressID", egressID,
				"avg load", fmt.Sprintf("%.2f%% (%.2f/%v)", avg, avg*float64(numCPUs)/100, numCPUs),
				"max load", fmt.Sprintf("%.2f%% (%.2f/%v)", max, max*float64(numCPUs)/100, numCPUs),
			)
			return
		default:
			cpuInfo, _ := cpu.Get()
			total := float64(cpuInfo.Total - prev.Total)
			user := float64(cpuInfo.User-prev.User) / total * 100
			userTotal += user
			if user > userMax {
				userMax = user
			}

			system := float64(cpuInfo.System-prev.System) / total * 100
			systemTotal += system
			if system > systemMax {
				systemMax = system
			}

			idle := float64(cpuInfo.Idle-prev.Idle) / total * 100
			idleTotal += idle
			if idle < idleMin {
				idleMin = idle
			}

			count++
			prev = cpuInfo
		}
	}
}
