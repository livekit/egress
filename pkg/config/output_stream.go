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
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

type StreamConfig struct {
	outputConfig

	Urls       []string
	StreamInfo map[string]*livekit.StreamInfo
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

	conf.StreamInfo = make(map[string]*livekit.StreamInfo)
	var streamInfoList []*livekit.StreamInfo
	for _, rawUrl := range urls {
		url, redacted, err := p.ValidateUrl(rawUrl, outputType)
		if err != nil {
			return nil, err
		}

		conf.Urls = append(conf.Urls, url)

		info := &livekit.StreamInfo{Url: redacted}
		conf.StreamInfo[url] = info
		streamInfoList = append(streamInfoList, info)
	}

	switch outputType {
	case types.OutputTypeRTMP:
		p.AudioOutCodec = types.MimeTypeAAC
		p.VideoOutCodec = types.MimeTypeH264

	case types.OutputTypeRaw:
		p.AudioOutCodec = types.MimeTypeRawAudio
	}

	return conf, nil
}
