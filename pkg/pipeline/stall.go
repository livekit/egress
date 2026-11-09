// Copyright 2026 LiveKit, Inc.
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

package pipeline

import (
	"sort"
	"time"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

// vars to allow tests to shorten them
var (
	stallCheckInterval = time.Second
	stallThreshold     = time.Second * 10
)

// startStallMonitor logs when a non-live pipeline's position stops advancing, which nothing else reports until EOS
func (c *Controller) startStallMonitor() {
	if c.Live {
		return
	}

	outputs := make([]string, 0, len(c.Outputs))
	reportsPosition := false
	for egressType := range c.Outputs {
		outputs = append(outputs, string(egressType))
		// splitmuxsink answers a position query per fragment, or not at all
		if egressType != types.EgressTypeSegments {
			reportsPosition = true
		}
	}
	sort.Strings(outputs)
	if !reportsPosition {
		logger.Debugw("stall monitor disabled, no output reports a position", "outputs", outputs)
		return
	}

	go c.monitorStall(outputs)
}

func (c *Controller) monitorStall(outputs []string) {
	select {
	case <-c.playing.Watch():
	case <-c.stopped.Watch():
		return
	case <-c.eosSent.Watch():
		return
	}

	ticker := time.NewTicker(stallCheckInterval)
	defer ticker.Stop()

	d := newStallDetector(time.Now(), stallThreshold)
	for {
		select {
		case <-c.stopped.Watch():
			return
		case <-c.eosSent.Watch():
			return
		case now := <-ticker.C:
			pos, ok := c.p.PlayheadPosition()
			switch event, stalledFor := d.observe(now, pos, ok); event {
			case stallStarted:
				logger.Warnw("pipeline output stalled", nil,
					"stalledFor", stalledFor,
					"position", d.position,
					"requestType", c.RequestType,
					"sourceType", c.SourceType,
					"outputs", outputs,
				)
			case stallEnded:
				logger.Infow("pipeline output resumed",
					"stalledFor", stalledFor,
					"position", d.position,
					"requestType", c.RequestType,
				)
			}
		}
	}
}

type stallEvent int

const (
	stallNone stallEvent = iota
	stallStarted
	stallEnded
)

// stallDetector reports once when the position has not advanced for threshold, and once when it advances again
type stallDetector struct {
	threshold  time.Duration
	position   time.Duration
	advancedAt time.Time
	stalled    bool
}

func newStallDetector(start time.Time, threshold time.Duration) *stallDetector {
	return &stallDetector{threshold: threshold, advancedAt: start}
}

// observe takes a position reading, a failed one counting as no progress, and returns any change with the time
// since the position last advanced
func (d *stallDetector) observe(now time.Time, pos time.Duration, ok bool) (stallEvent, time.Duration) {
	stalledFor := now.Sub(d.advancedAt)
	if ok && pos > d.position {
		d.position, d.advancedAt = pos, now
		if d.stalled {
			d.stalled = false
			return stallEnded, stalledFor
		}
		return stallNone, 0
	}

	if !d.stalled && stalledFor >= d.threshold {
		d.stalled = true
		return stallStarted, stalledFor
	}
	return stallNone, stalledFor
}
