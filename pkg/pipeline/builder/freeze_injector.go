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
	"io"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/linkdata/deadlock"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/logger"
)

const freezeFrameInterval = 500 * time.Millisecond

// freezeInjector is the pass-through equivalent of the decoded path's
// videotestsrc fallback. Without a local encoder there is no way to generate
// filler video, so instead it caches the last H.264 keyframe seen on the real
// track and, while the fallback selector pad is active (publisher muted,
// disconnected, or stalled), re-pushes that keyframe on an auxiliary appsrc so
// splitmuxsink keeps cutting segments and the HLS playlist keeps advancing.
//
// Repeated IDRs form a valid H.264 stream, and since every injected frame is a
// keyframe, splitmuxsink splits exactly at the configured segment duration
// while frozen. PTS comes from the appsrc's do-timestamp (pipeline running
// time), which keeps it monotonic with the real track's synchronizer-driven
// PTS; the selector pad probes drop any small overlap on switchover.
type freezeInjector struct {
	src            *app.Source
	fallbackActive func() bool
	stallAfter     time.Duration

	mu      deadlock.Mutex
	frame   []byte
	capsSet bool

	lastRealBuffer atomic.Int64 // UnixNano of the last real track buffer
	frozen         atomic.Bool
	framesInjected atomic.Uint64
	lastStallWarn  atomic.Int64

	started core.Fuse
	stopped core.Fuse

	logger logger.Logger
}

func newFreezeInjector(src *app.Source, stallAfter time.Duration, fallbackActive func() bool) *freezeInjector {
	return &freezeInjector{
		src:            src,
		fallbackActive: fallbackActive,
		stallAfter:     stallAfter,
		logger:         logger.GetLogger().WithValues("component", "freeze_injector"),
	}
}

// OnRealBuffer must be called for every buffer flowing on the real track's
// parser pad. It feeds the stall detector.
func (f *freezeInjector) OnRealBuffer() {
	f.lastRealBuffer.Store(time.Now().UnixNano())
}

// CacheKeyframe stores a copy of the latest keyframe and the current stream
// caps. The injection loop starts on the first cached keyframe - before that
// there is nothing valid to inject.
func (f *freezeInjector) CacheKeyframe(buffer *gst.Buffer, pad *gst.Pad) {
	mapInfo := buffer.Map(gst.MapRead)
	if mapInfo == nil {
		return
	}
	data, err := io.ReadAll(mapInfo.Reader())
	buffer.Unmap()
	if err != nil {
		return
	}

	f.cache(data, pad.GetCurrentCaps())
}

func (f *freezeInjector) cache(data []byte, caps *gst.Caps) {
	if len(data) == 0 {
		return
	}

	f.mu.Lock()
	f.frame = data
	if !f.capsSet && caps != nil {
		f.src.SetCaps(caps)
		f.capsSet = true
	}
	f.mu.Unlock()

	f.started.Once(func() {
		go f.run()
	})
}

// Stop ends the injection loop. When sendEOS is set, the auxiliary appsrc
// emits EOS so pipeline shutdown can propagate through the selector when the
// fallback pad is the active one.
func (f *freezeInjector) Stop(sendEOS bool) {
	f.stopped.Once(func() {
		if sendEOS {
			if flow := f.src.EndStream(); flow != gst.FlowOK && flow != gst.FlowFlushing {
				f.logger.Warnw("unexpected flow return on freeze src EOS", nil, "flowReturn", flow.String())
			}
		}
	})
}

func (f *freezeInjector) run() {
	ticker := time.NewTicker(freezeFrameInterval)
	defer ticker.Stop()

	for {
		select {
		case <-f.stopped.Watch():
			return
		case <-ticker.C:
			if f.fallbackActive() {
				f.injectFrame()
			} else {
				f.checkStall()
			}
		}
	}
}

func (f *freezeInjector) injectFrame() {
	f.mu.Lock()
	data := f.frame
	capsSet := f.capsSet
	f.mu.Unlock()
	if data == nil || !capsSet {
		return
	}

	if f.frozen.CompareAndSwap(false, true) {
		f.logger.Infow("video freeze started, repeating last keyframe",
			"startedAt", time.Now().UnixNano())
	}

	buffer := gst.NewBufferFromBytes(data)
	buffer.SetDuration(gst.ClockTime(uint64(freezeFrameInterval)))
	if flow := f.src.PushBuffer(buffer); flow != gst.FlowOK {
		// flushing while the pipeline spins up or shuts down is expected
		if flow != gst.FlowFlushing {
			f.logger.Warnw("freeze frame push failed", nil, "flowReturn", flow.String())
		}
		return
	}
	f.framesInjected.Inc()
}

func (f *freezeInjector) checkStall() {
	if f.frozen.CompareAndSwap(true, false) {
		f.logger.Infow("video freeze ended, track resumed",
			"endedAt", time.Now().UnixNano(),
			"framesInjected", f.framesInjected.Load())
	}

	lastReal := f.lastRealBuffer.Load()
	if lastReal == 0 {
		return
	}
	stalledFor := time.Since(time.Unix(0, lastReal))
	if stalledFor <= f.stallAfter {
		return
	}

	// The track pad is still selected but nothing is flowing and no
	// mute/unsubscribe event fired. The appwriter's inactivity path should
	// have switched to the fallback already - log so this shows up.
	lastWarn := f.lastStallWarn.Load()
	if time.Since(time.Unix(0, lastWarn)) > f.stallAfter && f.lastStallWarn.CompareAndSwap(lastWarn, time.Now().UnixNano()) {
		f.logger.Warnw("video track stalled without mute event", nil,
			"stalledFor", stalledFor)
	}
}
