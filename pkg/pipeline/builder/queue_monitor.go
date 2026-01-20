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

package builder

import (
	"github.com/go-gst/go-gst/gst"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/logger"
)

// LeakyQueueMonitor tracks buffer flow through a leaky queue to detect dropped buffers.
// It uses pad probes to count buffers in and out, then calculates drops as:
// dropped = inCount - outCount - currentLevel
type LeakyQueueMonitor struct {
	name     string
	queue    *gst.Element
	inCount  atomic.Uint64
	outCount atomic.Uint64
	eosSeen  atomic.Bool
	eosIn    atomic.Uint64
	eosOut   atomic.Uint64
}

// NewLeakyQueueMonitor creates a monitor for the given queue element and attaches
// pad probes to track buffer flow.
func NewLeakyQueueMonitor(name string, queue *gst.Element) *LeakyQueueMonitor {
	m := &LeakyQueueMonitor{
		name:  name,
		queue: queue,
	}

	// Attach probe on sink pad to count incoming buffers
	sinkPad := queue.GetStaticPad("sink")
	if sinkPad != nil {
		sinkPad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
			m.inCount.Inc()
			return gst.PadProbeOK
		})
	} else {
		logger.Warnw("failed to get sink pad for queue monitor", nil, "queue", name)
	}

	// Attach probe on src pad to count outgoing buffers
	srcPad := queue.GetStaticPad("src")
	if srcPad != nil {
		srcPad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
			m.outCount.Inc()
			return gst.PadProbeOK
		})
		srcPad.AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			if event := info.GetEvent(); event != nil && event.Type() == gst.EventTypeEOS {
				if !m.eosSeen.Swap(true) {
					inCount := m.inCount.Load()
					outCount := m.outCount.Load()
					m.eosIn.Store(inCount)
					m.eosOut.Store(outCount)
					m.postEOSStats(inCount, outCount)
				}
				return gst.PadProbeRemove
			}
			return gst.PadProbeOK
		})
	} else {
		logger.Warnw("failed to get src pad for queue monitor", nil, "queue", name)
	}

	return m
}

// LeakyQueueStatsMessage is the element message name for leaky queue stats.
const LeakyQueueStatsMessage = "LeakyQueueStats"

func (m *LeakyQueueMonitor) postEOSStats(inCount, outCount uint64) {
	if m.queue == nil {
		return
	}
	dropped := uint64(0)
	if outCount <= inCount {
		dropped = inCount - outCount
	}

	st := gst.NewStructure(LeakyQueueStatsMessage)
	_ = st.SetValue("queue", m.name)
	_ = st.SetValue("in", inCount)
	_ = st.SetValue("out", outCount)
	_ = st.SetValue("dropped", dropped)
	msg := gst.NewElementMessage(m.queue, st)
	if msg == nil {
		logger.Debugw("failed to build leaky queue stats message", nil, "queue", m.name)
		return
	}
	if ok := m.queue.PostMessage(msg); !ok {
		logger.Debugw("failed to post leaky queue stats message", nil, "queue", m.name)
	}
}

// DroppedBuffers calculates the number of buffers dropped by the leaky queue.
// dropped = inCount - outCount - currentLevel
func (m *LeakyQueueMonitor) DroppedBuffers() uint64 {
	if m.eosSeen.Load() {
		inCount := m.eosIn.Load()
		outCount := m.eosOut.Load()
		if outCount > inCount {
			return 0
		}
		return inCount - outCount
	}

	inCount := m.inCount.Load()
	outCount := m.outCount.Load()
	currentLevel := m.currentLevelBuffers()

	// Sanity check: if outCount + currentLevel > inCount, return 0
	if outCount+currentLevel > inCount {
		return 0
	}

	return inCount - outCount - currentLevel
}

// currentLevelBuffers reads the current-level-buffers property from the queue
func (m *LeakyQueueMonitor) currentLevelBuffers() uint64 {
	if m.queue == nil {
		return 0
	}

	val, err := m.queue.GetProperty("current-level-buffers")
	if err != nil {
		logger.Debugw("failed to get current-level-buffers", err, "queue", m.name)
		return 0
	}

	// The property returns uint
	if level, ok := val.(uint); ok {
		return uint64(level)
	}

	return 0
}

// Name returns the name of the monitored queue
func (m *LeakyQueueMonitor) Name() string {
	return m.name
}

// Stats returns the current monitoring stats
func (m *LeakyQueueMonitor) Stats() (inCount, outCount, currentLevel, dropped uint64) {
	if m.eosSeen.Load() {
		inCount = m.eosIn.Load()
		outCount = m.eosOut.Load()
		currentLevel = 0
	} else {
		inCount = m.inCount.Load()
		outCount = m.outCount.Load()
		currentLevel = m.currentLevelBuffers()
	}

	if outCount+currentLevel > inCount {
		dropped = 0
	} else {
		dropped = inCount - outCount - currentLevel
	}

	return
}
