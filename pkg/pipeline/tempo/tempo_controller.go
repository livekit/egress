package tempo

import (
	"sync"
	"time"

	"github.com/livekit/protocol/logger"
)

type Controller struct {
	sync.Mutex
	pendingOffset time.Duration // backlog of SRs generated offsets
	currentOffset time.Duration

	driftDetectedCallback func(time.Duration)
}

func (tc *Controller) EnqueueDrift(drift time.Duration) {
	if drift == 0 {
		return
	}

	tc.Lock()
	var cb func(time.Duration)

	logger.Debugw("tempo controller, adding drift")
	if tc.currentOffset == 0 {
		tc.currentOffset = drift
		if tc.driftDetectedCallback != nil {
			cb = tc.driftDetectedCallback
		}
	} else {
		tc.pendingOffset += drift
	}

	tc.Unlock()

	if cb != nil {
		cb(drift)
	}
}

func (tc *Controller) DriftProcessed() {
	logger.Debugw("controller, drift processed")

	tc.Lock()
	var currentOffset time.Duration
	var cb func(time.Duration)

	if tc.pendingOffset != 0 {
		tc.currentOffset = tc.pendingOffset
		tc.pendingOffset = 0
		if tc.driftDetectedCallback != nil {
			cb = tc.driftDetectedCallback
			currentOffset = tc.currentOffset
		}
	} else {
		tc.currentOffset = 0
	}
	tc.Unlock()

	if cb != nil {
		cb(currentOffset)
	}
}

func (tc *Controller) OnDriftDetectedCallback(cb func(time.Duration)) {
	logger.Debugw("setting drift detected callback")

	tc.Lock()
	tc.driftDetectedCallback = cb
	cw := tc.currentOffset
	tc.Unlock()

	if cw != 0 {
		cb(cw)
	}
}
