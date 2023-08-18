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
	b             *gstreamer.Bin
	outputType    types.OutputType
	bins          map[string]*gstreamer.Bin
	urls          map[string]string
	reconnections map[string]int
}

func BuildStreamBin(p *config.PipelineConfig, callbacks *gstreamer.Callbacks) (*StreamBin, *gstreamer.Bin, error) {
	b := gstreamer.NewBin("stream", gstreamer.BinTypeMuxed, callbacks)
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
		outputType:    o.OutputType,
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
	sinkBin, name, err := buildStreamSinkBin(sb.outputType, url, sb.b.Callbacks)
	if err != nil {
		return err
	}
	sb.mu.Lock()
	sb.bins[name] = sinkBin
	sb.urls[name] = url
	sb.mu.Unlock()

	return sb.b.AddSinkBin(sinkBin)
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

func buildStreamSinkBin(protocol types.OutputType, url string, callbacks *gstreamer.Callbacks) (*gstreamer.Bin, string, error) {
	name := utils.NewGuid("")
	b := gstreamer.NewBin(name, gstreamer.BinTypeMuxed, callbacks)

	queue, err := gst.NewElementWithName("queue", fmt.Sprintf("stream_queue_%s", name))
	if err != nil {
		return nil, "", errors.ErrGstPipelineError(err)
	}
	queue.SetArg("leaky", "downstream")

	var sink *gst.Element
	switch protocol {
	case types.OutputTypeRTMP:
		sink, err = gst.NewElementWithName("rtmp2sink", fmt.Sprintf("rtmp2sink_%s", name))
		if err != nil {
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

	return b, name, nil
}
