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
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

func (p *PipelineConfig) applyPreset(preset livekit.EncodingOptionsPreset) {
	switch preset {
	case livekit.EncodingOptionsPreset_H264_720P_30:
		p.Width = 1280
		p.Height = 720
		p.VideoBitrate = 3000

	case livekit.EncodingOptionsPreset_H264_720P_60:
		p.Width = 1280
		p.Height = 720
		p.Framerate = 60

	case livekit.EncodingOptionsPreset_H264_1080P_30:
		// default

	case livekit.EncodingOptionsPreset_H264_1080P_60:
		p.Framerate = 60
		p.VideoBitrate = 6000

	case livekit.EncodingOptionsPreset_PORTRAIT_H264_720P_30:
		p.Width = 720
		p.Height = 1280
		p.VideoBitrate = 3000

	case livekit.EncodingOptionsPreset_PORTRAIT_H264_720P_60:
		p.Width = 720
		p.Height = 1280
		p.Framerate = 60

	case livekit.EncodingOptionsPreset_PORTRAIT_H264_1080P_30:
		p.Width = 1080
		p.Height = 1920

	case livekit.EncodingOptionsPreset_PORTRAIT_H264_1080P_60:
		p.Width = 1080
		p.Height = 1920
		p.Framerate = 60
		p.VideoBitrate = 6000
	}
}

func (p *PipelineConfig) applyAdvanced(advanced *livekit.EncodingOptions) error {
	// audio
	switch advanced.AudioCodec {
	case livekit.AudioCodec_OPUS:
		p.AudioOutCodec = types.MimeTypeOpus
	case livekit.AudioCodec_AAC:
		p.AudioOutCodec = types.MimeTypeAAC
	}

	if advanced.AudioBitrate != 0 {
		p.AudioBitrate = advanced.AudioBitrate
	}
	if advanced.AudioFrequency != 0 {
		p.AudioFrequency = advanced.AudioFrequency
	}

	// video
	switch advanced.VideoCodec {
	case livekit.VideoCodec_H264_BASELINE:
		p.VideoOutCodec = types.MimeTypeH264
		p.VideoProfile = types.ProfileBaseline

	case livekit.VideoCodec_H264_MAIN:
		p.VideoOutCodec = types.MimeTypeH264

	case livekit.VideoCodec_H264_HIGH:
		p.VideoOutCodec = types.MimeTypeH264
		p.VideoProfile = types.ProfileHigh
	}

	if advanced.Width > 0 {
		if advanced.Width < 16 || advanced.Width%2 == 1 {
			return errors.ErrInvalidInput("width")
		}
		p.Width = advanced.Width
	}

	if advanced.Height > 0 {
		if advanced.Height < 16 || advanced.Height%2 == 1 {
			return errors.ErrInvalidInput("height")
		}
		p.Height = advanced.Height
	}

	switch advanced.Depth {
	case 0:
	case 8, 16, 24:
		p.Depth = advanced.Depth
	default:
		return errors.ErrInvalidInput("depth")
	}

	if advanced.Framerate != 0 {
		p.Framerate = advanced.Framerate
	}
	if advanced.VideoBitrate != 0 {
		p.VideoBitrate = advanced.VideoBitrate
	}
	if advanced.KeyFrameInterval != 0 {
		p.KeyFrameInterval = advanced.KeyFrameInterval
	}

	return nil
}
