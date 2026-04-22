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

//go:build integration

package test

import (
	"time"

	"github.com/livekit/egress/pkg/types"
)

var (
	samples = map[types.MimeType]string{
		types.MimeTypeOpus: "/media-samples/avsync_minmotion_livekit_audio_48k_120s.ogg",
		types.MimeTypeH264: "/media-samples/avsync_minmotion_livekit_video_1080p25_120s.h264",
		types.MimeTypeVP8:  "/media-samples/avsync_minmotion_livekit_1080p24_vp8.ivf",
		types.MimeTypeVP9:  "/media-samples/avsync_minmotion_livekit_1080p24_vp9.ivf",
		types.MimeTypePCMU: "/media-samples/avsync_minmotion_livekit_audio_8k_120s_pcmu.wav",
		types.MimeTypePCMA: "/media-samples/avsync_minmotion_livekit_audio_8k_120s_pcma.wav",
	}

	frameDurations = map[types.MimeType]time.Duration{
		types.MimeTypeH264: time.Microsecond * 41667,
		types.MimeTypeVP8:  time.Microsecond * 41667,
		types.MimeTypeVP9:  time.Microsecond * 41667,
		types.MimeTypePCMU: time.Millisecond * 20,
		types.MimeTypePCMA: time.Millisecond * 20,
	}
)
