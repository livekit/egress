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
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/protocol/logger"
)

const (
	captureContinuityInterval = 5 * time.Second

	webAudioSrcName  = "web_audio_src"
	webVideoSrcName  = "web_video_src"
	webVideoRateName = "web_video_rate"
)

type elementGetter interface {
	GetElementByName(name string) (*gst.Element, error)
}

// captureContinuity reports the events that shift audio against video in the output:
// gaps flagged by the capture sources, and samples or frames the rate elements insert or drop.
type captureContinuity struct {
	audioDiscont *atomic.Uint64
	videoDiscont *atomic.Uint64

	audioRate *gst.Element
	videoRate *gst.Element
}

// WatchCaptureContinuity logs the web source's capture gaps and rate corrections every
// captureContinuityInterval until the pipeline stops.
func WatchCaptureContinuity(p *gstreamer.Pipeline) {
	c := newCaptureContinuity(p)

	done := make(chan struct{})
	var once sync.Once
	p.AddOnStop(func() error {
		once.Do(func() { close(done) })
		return nil
	})

	go func() {
		ticker := time.NewTicker(captureContinuityInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				logger.Debugw("capture continuity", c.fields()...)
			}
		}
	}()
}

func newCaptureContinuity(p elementGetter) *captureContinuity {
	c := &captureContinuity{
		audioDiscont: countDiscont(p, webAudioSrcName),
		videoDiscont: countDiscont(p, webVideoSrcName),
	}
	c.audioRate, _ = p.GetElementByName("audio_rate")
	c.videoRate, _ = p.GetElementByName(webVideoRateName)
	return c
}

// countDiscont counts DISCONT buffers leaving the element's src pad, after its first buffer.
func countDiscont(p elementGetter, elementName string) *atomic.Uint64 {
	e, err := p.GetElementByName(elementName)
	if err != nil {
		return nil
	}
	pad := e.GetStaticPad("src")
	if pad == nil {
		return nil
	}

	count := atomic.NewUint64(0)
	started := atomic.NewBool(false)
	pad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		if buf == nil {
			return gst.PadProbeOK
		}
		if started.Swap(true) && buf.HasFlags(gst.BufferFlagDiscont) {
			count.Inc()
		}
		return gst.PadProbeOK
	})
	return count
}

func (c *captureContinuity) fields() []any {
	var fields []any
	if c.audioDiscont != nil {
		fields = append(fields, "audioCaptureGaps", c.audioDiscont.Load())
	}
	if c.videoDiscont != nil {
		fields = append(fields, "videoCaptureGaps", c.videoDiscont.Load())
	}
	fields = appendCounter(fields, c.audioRate, "add", "audioSamplesAdded")
	fields = appendCounter(fields, c.audioRate, "drop", "audioSamplesDropped")
	fields = appendCounter(fields, c.videoRate, "duplicate", "videoFramesDuplicated")
	fields = appendCounter(fields, c.videoRate, "drop", "videoFramesDropped")
	return fields
}

func appendCounter(fields []any, e *gst.Element, property, key string) []any {
	if e == nil {
		return fields
	}
	v, err := e.GetProperty(property)
	if err != nil {
		return fields
	}
	if n, ok := v.(uint64); ok {
		fields = append(fields, key, n)
	}
	return fields
}
