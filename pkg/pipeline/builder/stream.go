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
	"fmt"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
)

type StreamBin struct {
	mu         sync.RWMutex
	pipeline   *gstreamer.Pipeline
	b          *gstreamer.Bin
	outputType types.OutputType
	sinks      map[string]*StreamSink
}

type StreamSink struct {
	bin            *gstreamer.Bin
	sink           *gst.Element
	url            string
	reconnections  int
	disconnectedAt time.Time
	failed         bool
}

func BuildStreamBin(pipeline *gstreamer.Pipeline, p *config.PipelineConfig) (*StreamBin, *gstreamer.Bin, error) {
	b := pipeline.NewBin("stream")
	o := p.GetStreamConfig()

	var mux *gst.Element
	var err error
	switch o.OutputType {
	case types.OutputTypeRTMP:
		mux, err = gst.NewElement("flvmux")
		if err != nil {
			return nil, nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("streamable", true); err != nil {
			return nil, nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("skip-backwards-streams", true); err != nil {
			return nil, nil, errors.ErrGstPipelineError(err)
		}
		// add latency to give time for flvmux to receive and order packets from both streams
		if err = mux.SetProperty("latency", config.Latency); err != nil {
			return nil, nil, errors.ErrGstPipelineError(err)
		}

		b.SetGetSrcPad(func(name string) *gst.Pad {
			return mux.GetRequestPad(name)
		})

	case types.OutputTypeSRT:
		mux, err = gst.NewElement("mpegtsmux")
		if err != nil {
			return nil, nil, errors.ErrGstPipelineError(err)
		}

	default:
		err = errors.ErrInvalidInput("output type")
	}
	if err != nil {
		return nil, nil, err
	}

	tee, err := gst.NewElement("tee")
	if err != nil {
		return nil, nil, errors.ErrGstPipelineError(err)
	}
	if err = tee.SetProperty("allow-not-linked", true); err != nil {
		return nil, nil, errors.ErrGstPipelineError(err)
	}

	if err = b.AddElements(mux, tee); err != nil {
		return nil, nil, err
	}

	sb := &StreamBin{
		b:          b,
		outputType: o.OutputType,
		sinks:      make(map[string]*StreamSink),
	}

	for _, url := range o.Urls {
		if err = sb.AddStream(url); err != nil {
			return nil, nil, err
		}
	}

	return sb, b, nil
}

func (sb *StreamBin) AddStream(url string) error {
	name := utils.NewGuid("")
	b := sb.b.NewBin(name)

	queue, err := gstreamer.BuildQueue(fmt.Sprintf("queue_%s", name), config.Latency, true)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	var sink *gst.Element
	switch sb.outputType {
	case types.OutputTypeRTMP:
		sink, err = gst.NewElementWithName("rtmp2sink", fmt.Sprintf("rtmp2sink_%s", name))
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = sink.Set("location", url); err != nil {
			return errors.ErrGstPipelineError(err)
		}

	case types.OutputTypeSRT:
		sink, err = gst.NewElementWithName("srtsink", fmt.Sprintf("srtsink_%s", name))
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = sink.SetProperty("uri", url); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = sink.SetProperty("wait-for-connection", false); err != nil {
			return errors.ErrGstPipelineError(err)
		}

	default:
		return errors.ErrInvalidInput("output type")
	}

	// GstBaseSink properties
	if err = sink.SetProperty("async", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("sync", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = b.AddElements(queue, sink); err != nil {
		return err
	}

	ss := &StreamSink{
		bin:  b,
		sink: sink,
		url:  url,
	}

	// add a proxy pad between the queue and sink to prevent errors from propagating upstream
	b.SetLinkFunc(func() error {
		proxy := gst.NewGhostPad(fmt.Sprintf("proxy_%s", name), sink.GetStaticPad("sink"))
		proxy.Ref()
		proxy.ActivateMode(gst.PadModePush, true)

		switch sb.outputType {
		case types.OutputTypeRTMP:
			proxy.SetChainFunction(func(self *gst.Pad, _ *gst.Object, buffer *gst.Buffer) gst.FlowReturn {
				buffer.Ref()
				links, _ := self.GetInternalLinks()
				switch {
				case len(links) != 1:
					return gst.FlowNotLinked
				case links[0].Push(buffer) == gst.FlowEOS:
					return gst.FlowEOS
				default:
					return gst.FlowOK
				}
			})
		case types.OutputTypeSRT:
			proxy.SetChainListFunction(func(self *gst.Pad, _ *gst.Object, list *gst.BufferList) gst.FlowReturn {
				list.Ref()
				if ss.failed {
					return gst.FlowOK
				}
				links, _ := self.GetInternalLinks()
				if len(links) != 1 {
					return gst.FlowNotLinked
				}
				switch links[0].PushList(list) {
				case gst.FlowEOS:
					return gst.FlowEOS
				case gst.FlowError:
					ss.failed = true
				}
				return gst.FlowOK
			})
		}

		// link queue to sink
		if padReturn := queue.GetStaticPad("src").Link(proxy.Pad); padReturn != gst.PadLinkOK {
			return errors.ErrPadLinkFailed(queue.GetName(), "proxy", padReturn.String())
		}
		return nil
	})

	sb.mu.Lock()
	sb.sinks[name] = ss
	sb.mu.Unlock()

	return sb.b.AddSinkBin(b)
}

func (sb *StreamBin) GetStreamUrl(name string) (string, error) {
	sb.mu.RLock()
	sink, ok := sb.sinks[name]
	sb.mu.RUnlock()
	if !ok {
		return "", errors.ErrStreamNotFound(name)
	}
	return sink.url, nil
}

func (sb *StreamBin) MaybeResetStream(name string, streamErr error) (bool, error) {
	sb.mu.Lock()
	sink := sb.sinks[name]
	sb.mu.Unlock()

	if sink == nil {
		return false, errors.ErrStreamNotFound(name)
	}

	s, err := sink.sink.GetProperty("stats")
	if err != nil {
		return false, err
	}
	values := s.(*gst.Structure).Values()
	outBytes := values["out-bytes-acked"].(uint64)

	if sink.reconnections == 0 && outBytes == 0 {
		// unable to connect, probably a bad stream key or url
		return false, nil
	}

	if outBytes > 0 {
		// first disconnection
		sink.disconnectedAt = time.Now()
		sink.reconnections = 0
	} else if time.Since(sink.disconnectedAt) > time.Second*30 {
		return false, nil
	}

	sink.reconnections++
	redacted, _ := utils.RedactStreamKey(sink.url)
	logger.Warnw("resetting stream", streamErr, "url", redacted)

	if err = sink.bin.SetState(gst.StateNull); err != nil {
		return false, err
	}
	if err = sink.bin.SetState(gst.StatePlaying); err != nil {
		return false, err
	}

	return true, nil
}

func (sb *StreamBin) RemoveStream(url string) error {
	sb.mu.Lock()
	var name string
	var sink *StreamSink
	for n, s := range sb.sinks {
		if s.url == url {
			name = n
			sink = s
			break
		}
	}
	if sink == nil {
		sb.mu.Unlock()
		return errors.ErrStreamNotFound(url)
	}
	delete(sb.sinks, name)
	sb.mu.Unlock()

	return sb.b.RemoveSinkBin(name)
}
