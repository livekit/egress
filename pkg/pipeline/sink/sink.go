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

package sink

import (
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

type Sink interface {
	Start() error
	AddEOSProbe()
	EOSReceived() bool
	Err() chan error
	UploadManifest(string) (string, bool, error)
}

type sink struct {
	bin         *gstreamer.Bin
	eosReceived atomic.Bool
	close       func() error
	closeErr    chan error
}

func NewSink(
	p *gstreamer.Pipeline,
	conf *config.PipelineConfig,
	egressType types.EgressType,
	o config.OutputConfig,
	callbacks *gstreamer.Callbacks,
	monitor *stats.HandlerMonitor,
) (Sink, error) {

	var s Sink
	var base *sink
	switch egressType {
	case types.EgressTypeFile:
		fileSink, err := NewFileSink(conf, o.(*config.FileConfig), monitor)
		if err != nil {
			return nil, err
		}
		fileBin, err := builder.BuildFileBin(p, conf)
		if err != nil {
			return nil, err
		}
		base = &sink{
			bin:      fileBin,
			close:    fileSink.close,
			closeErr: make(chan error, 1),
		}
		fileSink.sink = base
		s = fileSink

	case types.EgressTypeSegments:
		segmentSink, err := NewSegmentSink(conf, o.(*config.SegmentConfig), callbacks, monitor)
		if err != nil {
			return nil, err
		}
		segmentBin, err := builder.BuildSegmentBin(p, conf)
		if err != nil {
			return nil, err
		}
		base = &sink{
			bin:      segmentBin,
			close:    segmentSink.close,
			closeErr: make(chan error, 1),
		}
		segmentSink.sink = base
		s = segmentSink

	case types.EgressTypeStream:
		streamBin, err := builder.BuildStreamBin(p, o.(*config.StreamConfig))
		if err != nil {
			return nil, err
		}
		streamSink, err := NewStreamSink(streamBin, o.(*config.StreamConfig))
		if err != nil {
			return nil, err
		}
		base = &sink{
			bin:      streamBin.Bin,
			close:    streamSink.close,
			closeErr: make(chan error, 1),
		}
		streamSink.sink = base
		s = streamSink

	case types.EgressTypeWebsocket:
		websocketSink, err := NewWebsocketSink(o.(*config.StreamConfig), types.MimeTypeRawAudio, callbacks)
		if err != nil {
			return nil, err
		}
		websocketBin, err := builder.BuildWebsocketBin(p, websocketSink.sinkCallbacks)
		if err != nil {
			return nil, err
		}
		base = &sink{
			bin:      websocketBin,
			close:    websocketSink.close,
			closeErr: make(chan error, 1),
		}
		websocketSink.sink = base
		s = websocketSink

	case types.EgressTypeImages:
		imageSink, err := NewImageSink(conf, o.(*config.ImageConfig), callbacks, monitor)
		if err != nil {
			return nil, err
		}
		imageBin, err := builder.BuildImageBin(o.(*config.ImageConfig), p, conf)
		if err != nil {
			return nil, err
		}
		base = &sink{
			bin:      imageBin,
			close:    imageSink.close,
			closeErr: make(chan error, 1),
		}
		imageSink.sink = base
		s = imageSink

	default:
		return nil, nil
	}

	if err := p.AddSinkBin(base.bin); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *sink) AddEOSProbe() {
	if err := s.bin.AddOnEOSReceived(func() {
		logger.Debugw("sink received EOS", "bin", s.bin.GetName())
		s.eosReceived.Store(true)
		s.closeErr <- s.close()
		logger.Debugw("sink closed", "bin", s.bin.GetName())
	}); err != nil {
		logger.Errorw("failed to add EOS probe", err)
	}
}

func (s *sink) EOSReceived() bool {
	return s.eosReceived.Load()
}

func (s *sink) Err() chan error {
	return s.closeErr
}
