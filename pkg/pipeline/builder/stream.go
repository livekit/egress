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

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
)

type StreamBin struct {
	mu            sync.RWMutex
	pipeline      *gstreamer.Pipeline
	b             *gstreamer.Bin
	outputType    types.OutputType
	bins          map[string]*gstreamer.Bin
	urls          map[string]string
	reconnections map[string]int
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
		if err = mux.SetProperty("latency", p.Latency); err != nil {
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
		b:             b,
		outputType:    o.OutputType,
		bins:          make(map[string]*gstreamer.Bin),
		urls:          make(map[string]string),
		reconnections: make(map[string]int),
	}

	for _, url := range o.Urls {
		if err = sb.AddStream(url); err != nil {
			return nil, nil, err
		}
	}

	return sb, b, nil
}

func (sb *StreamBin) GetStreamUrl(name string) (string, error) {
	sb.mu.RLock()
	url, ok := sb.urls[name]
	sb.mu.RUnlock()
	if !ok {
		return "", errors.ErrStreamNotFound(name)
	}
	return url, nil
}

func (sb *StreamBin) AddStream(url string) error {
	sinkBin, name, err := sb.buildStreamSinkBin(url)
	if err != nil {
		return err
	}
	sb.mu.Lock()
	sb.bins[name] = sinkBin
	sb.urls[name] = url
	sb.mu.Unlock()

	return sb.b.AddSinkBin(sinkBin)
}

func (sb *StreamBin) buildStreamSinkBin(url string) (*gstreamer.Bin, string, error) {
	name := utils.NewGuid("")
	b := sb.b.NewBin(name)

	queue, err := gst.NewElementWithName("queue", fmt.Sprintf("queue_%s", name))
	if err != nil {
		return nil, "", errors.ErrGstPipelineError(err)
	}
	queue.SetArg("leaky", "downstream")

	var sink *gst.Element
	switch sb.outputType {
	case types.OutputTypeRTMP:
		sink, err = gst.NewElementWithName("rtmp2sink", fmt.Sprintf("rtmp2sink_%s", name))
		if err != nil {
			return nil, "", errors.ErrGstPipelineError(err)
		}
		if err = sink.SetProperty("async", false); err != nil {
			return nil, "", errors.ErrGstPipelineError(err)
		}
		if err = sink.SetProperty("sync", false); err != nil {
			return nil, "", errors.ErrGstPipelineError(err)
		}
		if err = sink.SetProperty("async-connect", false); err != nil {
			return nil, "", errors.ErrGstPipelineError(err)
		}
		if err = sink.Set("location", url); err != nil {
			return nil, "", errors.ErrGstPipelineError(err)
		}

	default:
		return nil, "", errors.ErrInvalidInput("output type")
	}

	if err = b.AddElements(queue, sink); err != nil {
		return nil, "", err
	}

	b.SetLinkFunc(func() error {
		proxy := gst.NewGhostPad("proxy", sink.GetStaticPad("sink"))

		// Proxy isn't saved/stored anywhere, so we need to call ref.
		// It is later released in RemoveSink
		proxy.Ref()

		// Intercept flows from rtmp2sink. Anything besides EOS will be ignored
		proxy.SetChainFunction(func(self *gst.Pad, _ *gst.Object, buffer *gst.Buffer) gst.FlowReturn {
			// Buffer gets automatically unreferenced by go-gst.
			// Without referencing it here, it will sometimes be garbage collected before being written
			buffer.Ref()

			internal, _ := self.GetInternalLinks()
			if len(internal) != 1 {
				return gst.FlowNotLinked
			}

			if internal[0].Push(buffer) == gst.FlowEOS {
				return gst.FlowEOS
			}
			return gst.FlowOK
		})
		proxy.ActivateMode(gst.PadModePush, true)

		if padReturn := queue.GetStaticPad("src").Link(proxy.Pad); padReturn != gst.PadLinkOK {
			return errors.ErrPadLinkFailed(queue.GetName(), "proxy", padReturn.String())
		}
		return nil
	})

	return b, name, nil
}

func (sb *StreamBin) ResetStream(name string, streamErr error) (bool, error) {
	sb.mu.Lock()
	sinkBin := sb.bins[name]
	url := sb.urls[name]
	reconnections := sb.reconnections[name] + 1
	sb.reconnections[name] = reconnections
	sb.mu.Unlock()

	if sinkBin == nil {
		return false, errors.ErrStreamNotFound(name)
	}
	if reconnections > 3 {
		return false, nil
	}

	redacted, _ := utils.RedactStreamKey(url)
	logger.Warnw("resetting stream", streamErr, "url", redacted)

	if err := sinkBin.SetState(gst.StateNull); err != nil {
		return false, err
	}
	if err := sinkBin.SetState(gst.StatePlaying); err != nil {
		return false, err
	}
	return true, nil
}

func (sb *StreamBin) RemoveStream(url string) error {
	sb.mu.Lock()
	name := sb.getStreamName(url)
	if name == "" {
		sb.mu.Unlock()
		return errors.ErrStreamNotFound(url)
	}
	delete(sb.urls, name)
	delete(sb.reconnections, name)
	sb.mu.Unlock()

	_, err := sb.b.RemoveSinkBin(name)
	return err
}

func (sb *StreamBin) getStreamName(url string) string {
	for name, u := range sb.urls {
		if u == url {
			return name
		}
	}
	return ""
}
