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
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

type Sink interface {
	Start() error
	AddEOSProbe()
	EOSReceived() bool
	Close() error
	UploadManifest(string) (string, bool, error)
}

type base struct {
	bin         *gstreamer.Bin
	eosReceived atomic.Bool
}

func NewSink(
	p *gstreamer.Pipeline,
	conf *config.PipelineConfig,
	egressType types.EgressType,
	o config.OutputConfig,
	callbacks *gstreamer.Callbacks,
	monitor *stats.HandlerMonitor,
) (Sink, error) {

	switch egressType {
	case types.EgressTypeFile:
		return newFileSink(p, conf, o.(*config.FileConfig), monitor)

	case types.EgressTypeSegments:
		return newSegmentSink(p, conf, o.(*config.SegmentConfig), callbacks, monitor)

	case types.EgressTypeStream:
		return newStreamSink(p, conf, o.(*config.StreamConfig))

	case types.EgressTypeWebsocket:
		return newWebsocketSink(p, o.(*config.StreamConfig), types.MimeTypeRawAudio, callbacks)

	case types.EgressTypeImages:
		return newImageSink(p, conf, o.(*config.ImageConfig), callbacks, monitor)

	default:
		return nil, errors.ErrInvalidInput("output type")
	}
}

func (s *base) AddEOSProbe() {
	if err := s.bin.AddOnEOSReceived(func() {
		logger.Debugw("eos received", "sink", s.bin.GetName())
		s.eosReceived.Store(true)
	}); err != nil {
		logger.Errorw("failed to add EOS probe", err)
	}
}

func (s *base) EOSReceived() bool {
	return s.eosReceived.Load()
}
