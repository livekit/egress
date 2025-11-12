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

package config

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type StreamConfig struct {
	outputConfig

	// url -> Stream
	Streams sync.Map

	twitchTemplate string
}

type Stream struct {
	Name        string // gstreamer stream ID
	ParsedUrl   string // parsed/validated url
	RedactedUrl string // url with stream key removed
	StreamID    string // stream ID used by rtmpconnection
	StreamInfo  *livekit.StreamInfo

	lastRetryUpdate atomic.Int64
}

func (p *PipelineConfig) GetStreamConfig() *StreamConfig {
	o, ok := p.Outputs[types.EgressTypeStream]
	if !ok || len(o) == 0 {
		return nil
	}
	return o[0].(*StreamConfig)
}

func (p *PipelineConfig) GetWebsocketConfig() *StreamConfig {
	o, ok := p.Outputs[types.EgressTypeWebsocket]
	if !ok || len(o) == 0 {
		return nil
	}
	return o[0].(*StreamConfig)
}

func (p *PipelineConfig) getStreamConfig(outputType types.OutputType, urls []string) (*StreamConfig, error) {
	conf := &StreamConfig{
		outputConfig: outputConfig{OutputType: outputType},
	}

	for _, rawUrl := range urls {
		_, err := conf.AddStream(rawUrl, outputType)
		if err != nil {
			return nil, err
		}
	}

	switch outputType {
	case types.OutputTypeRTMP:
		p.AudioOutCodec = types.MimeTypeAAC
		p.VideoOutCodec = types.MimeTypeH264

	case types.OutputTypeSRT:
		p.AudioOutCodec = types.MimeTypeAAC
		p.VideoOutCodec = types.MimeTypeH264

	case types.OutputTypeRaw:
		p.AudioOutCodec = types.MimeTypeRawAudio
	}

	return conf, nil
}

func (s *Stream) UpdateEndTime(endedAt int64) {
	s.StreamInfo.EndedAt = endedAt
	if s.StreamInfo.StartedAt == 0 {
		if s.StreamInfo.Status != livekit.StreamInfo_FAILED {
			logger.Warnw("stream missing start time", nil, "url", s.RedactedUrl)
		}
		s.StreamInfo.StartedAt = endedAt
	} else {
		s.StreamInfo.Duration = endedAt - s.StreamInfo.StartedAt
	}
}

func (s *Stream) ShouldSendRetryUpdate(now time.Time, minInterval time.Duration) bool {
	last := s.lastRetryUpdate.Load()
	if last == 0 || now.UnixNano()-last >= int64(minInterval) {
		s.lastRetryUpdate.Store(now.UnixNano())
		return true
	}
	return false
}
