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

package builder

import (
	"github.com/go-gst/go-gst/gst"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/logger"
)

// LeakyQueueMonitor tracks buffer flow through a leaky queue to detect dropped buffers.
// It uses pad probes to count buffers in and out, then calculates drops as:
// dropped = inCount - outCount
type LeakyQueueMonitor struct {
	name     string
	queue    *gst.Element
	inCount  atomic.Uint64
	outCount atomic.Uint64
	eosSeen  atomic.Bool
}

// NewLeakyQueueMonitor creates a monitor for the given queue element and attaches
// pad probes to track buffer flow.
func NewLeakyQueueMonitor(name string, queue *gst.Element) {
	m := &LeakyQueueMonitor{
		name:  name,
		queue: queue,
	}

	sinkPad := queue.GetStaticPad("sink")
	if sinkPad != nil {
		sinkPad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
			m.inCount.Inc()
			return gst.PadProbeOK
		})
	} else {
		logger.Warnw("failed to get sink pad for queue monitor", nil, "queue", name)
	}

	srcPad := queue.GetStaticPad("src")
	if srcPad != nil {
		srcPad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
			m.outCount.Inc()
			return gst.PadProbeOK
		})
		srcPad.AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			if event := info.GetEvent(); event != nil && event.Type() == gst.EventTypeEOS {
				if !m.eosSeen.Swap(true) {
					m.postEOSStats()
				}
				return gst.PadProbeRemove
			}
			return gst.PadProbeOK
		})
	} else {
		logger.Warnw("failed to get src pad for queue monitor", nil, "queue", name)
	}
}

const LeakyQueueStatsMessage = "LeakyQueueStats"

func (m *LeakyQueueMonitor) postEOSStats() {
	if m.queue == nil {
		return
	}
	inCount := m.inCount.Load()
	outCount := m.outCount.Load()
	dropped := uint64(0)
	if outCount <= inCount {
		dropped = inCount - outCount
	}

	st := gst.NewStructure(LeakyQueueStatsMessage)
	err := st.SetValue("queue", m.name)
	if err != nil {
		logger.Debugw("failed to set queue name", err, "queue", m.name)
		return
	}
	err = st.SetValue("in", inCount)
	if err != nil {
		logger.Debugw("failed to set in count", err, "queue", m.name)
		return
	}
	err = st.SetValue("out", outCount)
	if err != nil {
		logger.Debugw("failed to set out count", err, "queue", m.name)
		return
	}
	err = st.SetValue("dropped", dropped)
	if err != nil {
		logger.Debugw("failed to set dropped count", err, "queue", m.name)
		return
	}
	msg := gst.NewElementMessage(m.queue, st)
	if msg == nil {
		logger.Debugw("failed to build leaky queue stats message", nil, "queue", m.name)
		return
	}
	if ok := m.queue.PostMessage(msg); !ok {
		logger.Debugw("failed to post leaky queue stats message", nil, "queue", m.name)
	}
}

// Name returns the name of the monitored queue
func (m *LeakyQueueMonitor) Name() string {
	return m.name
}
