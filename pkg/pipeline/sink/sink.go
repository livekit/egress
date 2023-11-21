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
	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/pkg/types"
)

type Sink interface {
	Start() error
	Close() error
	Cleanup()
}

func CreateSinks(p *config.PipelineConfig, callbacks *gstreamer.Callbacks, monitor *stats.HandlerMonitor) (map[types.EgressType][]Sink, error) {
	sinks := make(map[types.EgressType][]Sink)
	for egressType, c := range p.Outputs {
		if len(c) == 0 {
			continue
		}

		var s Sink
		var err error
		switch egressType {
		case types.EgressTypeFile:
			o := c[0].(*config.FileConfig)

			u, err := uploader.New(o.UploadConfig, p.BackupStorage, monitor)
			if err != nil {
				return nil, err
			}

			s = newFileSink(u, p, o)

		case types.EgressTypeSegments:
			o := c[0].(*config.SegmentConfig)

			u, err := uploader.New(o.UploadConfig, p.BackupStorage, monitor)
			if err != nil {
				return nil, err
			}

			s, err = newSegmentSink(u, p, o, callbacks, monitor)
			if err != nil {
				return nil, err
			}

		case types.EgressTypeStream:
			// no sink needed

		case types.EgressTypeWebsocket:
			o := c[0].(*config.StreamConfig)

			s, err = newWebsocketSink(o, types.MimeTypeRawAudio, callbacks)
			if err != nil {
				return nil, err
			}
		case types.EgressTypeImages:
			for _, ci := range c {
				o := ci.(*config.ImageConfig)

				u, err := uploader.New(o.UploadConfig, p.BackupStorage, monitor)
				if err != nil {
					return nil, err
				}

				s, err = newImageSink(u, p, o, callbacks)
				if err != nil {
					return nil, err
				}
			}
		}

		if s != nil {
			sinks[egressType] = append(sinks[egressType], s)
		}
	}

	return sinks, nil
}
