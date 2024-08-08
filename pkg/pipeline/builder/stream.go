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
	stream         *config.Stream
	bin            *gstreamer.Bin
	sink           *gst.Element
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

	o.Streams.Range(func(_, stream any) bool {
		err = sb.AddStream(stream.(*config.Stream))
		return err == nil
	})
	if err != nil {
		return nil, nil, err
	}

	return sb, b, nil
}

func (sb *StreamBin) AddStream(stream *config.Stream) error {
	stream.Name = utils.NewGuid("")
	b := sb.b.NewBin(stream.Name)

	queue, err := gstreamer.BuildQueue(fmt.Sprintf("queue_%s", stream.Name), config.Latency, true)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	var sink *gst.Element
	switch sb.outputType {
	case types.OutputTypeRTMP:
		sink, err = gst.NewElementWithName("rtmp2sink", fmt.Sprintf("rtmp2sink_%s", stream.Name))
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = sink.Set("location", stream.ParsedUrl); err != nil {
			return errors.ErrGstPipelineError(err)
		}

	case types.OutputTypeSRT:
		sink, err = gst.NewElementWithName("srtsink", fmt.Sprintf("srtsink_%s", stream.Name))
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = sink.SetProperty("uri", stream.ParsedUrl); err != nil {
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
		stream: stream,
		bin:    b,
		sink:   sink,
	}

	// add a proxy pad between the queue and sink to prevent errors from propagating upstream
	b.SetLinkFunc(func() error {
		proxy := gst.NewGhostPad(fmt.Sprintf("proxy_%s", stream.Name), sink.GetStaticPad("sink"))
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
	sb.sinks[stream.Name] = ss
	sb.mu.Unlock()

	return sb.b.AddSinkBin(b)
}

func (sb *StreamBin) GetStream(name string) (*config.Stream, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	sink, ok := sb.sinks[name]
	if !ok {
		return nil, errors.ErrStreamNotFound(name)
	}
	return sink.stream, nil
}

func (sb *StreamBin) MaybeResetStream(stream *config.Stream, streamErr error) (bool, error) {
	sb.mu.Lock()
	sink, ok := sb.sinks[stream.Name]
	sb.mu.Unlock()
	if !ok {
		return false, errors.ErrStreamNotFound(stream.Name)
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
	logger.Warnw("resetting stream", streamErr, "url", sink.stream.RedactedUrl)

	if err = sink.bin.SetState(gst.StateNull); err != nil {
		return false, err
	}
	if err = sink.bin.SetState(gst.StatePlaying); err != nil {
		return false, err
	}

	return true, nil
}

func (sb *StreamBin) RemoveStream(stream *config.Stream) error {
	sb.mu.Lock()
	_, ok := sb.sinks[stream.Name]
	if !ok {
		sb.mu.Unlock()
		return errors.ErrStreamNotFound(stream.RedactedUrl)
	}
	delete(sb.sinks, stream.Name)
	sb.mu.Unlock()

	return sb.b.RemoveSinkBin(stream.Name)
}
