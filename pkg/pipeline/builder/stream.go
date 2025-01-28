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
	Bin *gstreamer.Bin

	mu         sync.RWMutex
	pipeline   *gstreamer.Pipeline
	outputType types.OutputType
}

type Stream struct {
	Conf *config.Stream
	Bin  *gstreamer.Bin

	sink           *gst.Element
	reconnections  int
	disconnectedAt time.Time
	failed         bool
}

func BuildStreamBin(pipeline *gstreamer.Pipeline, o *config.StreamConfig) (*StreamBin, error) {
	b := pipeline.NewBin("stream")

	var mux *gst.Element
	var err error
	switch o.OutputType {
	case types.OutputTypeRTMP:
		mux, err = gst.NewElement("flvmux")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("streamable", true); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("skip-backwards-streams", true); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		// add latency to give time for flvmux to receive and order packets from both streams
		if err = mux.SetProperty("latency", config.Latency); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		b.SetGetSrcPad(func(name string) *gst.Pad {
			return mux.GetRequestPad(name)
		})

	case types.OutputTypeSRT:
		mux, err = gst.NewElement("mpegtsmux")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

	default:
		err = errors.ErrInvalidInput("output type")
	}
	if err != nil {
		return nil, err
	}

	tee, err := gst.NewElement("tee")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = tee.SetProperty("allow-not-linked", true); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	if err = b.AddElements(mux, tee); err != nil {
		return nil, err
	}

	sb := &StreamBin{
		Bin:        b,
		outputType: o.OutputType,
	}

	return sb, nil
}

func (sb *StreamBin) BuildStream(stream *config.Stream) (*Stream, error) {
	stream.Name = utils.NewGuid("")
	b := sb.Bin.NewBin(stream.Name)

	queue, err := gstreamer.BuildQueue(fmt.Sprintf("queue_%s", stream.Name), config.Latency, true)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	var sink *gst.Element
	switch sb.outputType {
	case types.OutputTypeRTMP:
		sink, err = gst.NewElementWithName("rtmp2sink", fmt.Sprintf("rtmp2sink_%s", stream.Name))
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = sink.Set("location", stream.ParsedUrl); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = sink.SetProperty("async-connect", false); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

	case types.OutputTypeSRT:
		sink, err = gst.NewElementWithName("srtsink", fmt.Sprintf("srtsink_%s", stream.Name))
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = sink.SetProperty("uri", stream.ParsedUrl); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = sink.SetProperty("wait-for-connection", false); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

	default:
		return nil, errors.ErrInvalidInput("output type")
	}

	// GstBaseSink properties
	if err = sink.SetProperty("async", false); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("sync", false); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = b.AddElements(queue, sink); err != nil {
		return nil, err
	}

	ss := &Stream{
		Conf: stream,
		Bin:  b,
		sink: sink,
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

	return ss, nil
}

func (s *Stream) Reset(streamErr error) (bool, error) {
	structure, err := s.sink.GetProperty("stats")
	if err != nil {
		return false, err
	}
	values := structure.(*gst.Structure).Values()
	outBytes := values["out-bytes-acked"].(uint64)

	if s.reconnections == 0 && outBytes == 0 {
		// unable to connect, probably a bad stream key or url
		return false, nil
	}

	if outBytes > 0 {
		// first disconnection
		s.disconnectedAt = time.Now()
		s.reconnections = 0
	} else if time.Since(s.disconnectedAt) > time.Second*30 {
		return false, nil
	}

	s.reconnections++
	logger.Warnw("resetting stream", streamErr, "url", s.Conf.RedactedUrl)

	if err = s.Bin.SetState(gst.StateNull); err != nil {
		return false, err
	}
	if err = s.Bin.SetState(gst.StatePlaying); err != nil {
		return false, err
	}

	return true, nil
}
