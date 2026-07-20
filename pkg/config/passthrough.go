// Copyright 2026 LiveKit, Inc.
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
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/pkg/types"
)

// Video pass-through skips the decode/encode stages and forwards the published
// H.264 bitstream directly to the muxer. It is opt-in via the
// enable_video_passthrough deployment flag and only ever downgrades to the
// default transcoding pipeline, never the other way around.
//
// Eligibility is decided in two phases:
//
//  1. Request time (maybeEnableVideoPassthrough): the deployment flag is on,
//     the request subscribes to at most one video track (TrackComposite or
//     Participant - never composition), and no video encoding options were
//     requested. Requiring the request to carry no explicit video options also
//     guarantees no resolution/bitrate change is needed: the output follows
//     whatever the track publishes.
//  2. Output validation (disableVideoPassthrough callers in output.go) and
//     subscription time (updatePreInitStateLocked in track_worker.go): any
//     non-segment output, or an input codec that differs from the output
//     codec, falls back to transcoding with an informative log.

// maybeEnableVideoPassthrough marks the request as a pass-through candidate,
// clearing the forced VideoDecoding flag. Must be called after encoding
// options are applied and before outputs are parsed.
func (p *PipelineConfig) maybeEnableVideoPassthrough(hasPreset bool, advanced *livekit.EncodingOptions) {
	if !p.EnableVideoPassthrough || !p.VideoEnabled {
		return
	}

	switch p.RequestType {
	case types.RequestTypeTrackComposite, types.RequestTypeParticipant:
		// single-publisher SDK sources, at most one video track subscribed
	default:
		logger.Infow("video passthrough not eligible",
			"reason", "request type requires composition",
			"requestType", p.RequestType)
		return
	}

	if hasPreset {
		logger.Infow("video passthrough not eligible",
			"reason", "encoding preset requested")
		return
	}
	if advanced != nil && advancedHasVideoOverrides(advanced) {
		logger.Infow("video passthrough not eligible",
			"reason", "advanced video encoding options requested")
		return
	}

	p.VideoDecoding = false
	p.VideoPassthrough = true
	logger.Infow("video passthrough candidate",
		"requestType", p.RequestType)
}

// DisableVideoPassthrough reverts a pass-through candidate to the default
// decode path. Safe to call unconditionally.
func (p *PipelineConfig) DisableVideoPassthrough(reason string) {
	if !p.VideoPassthrough {
		return
	}
	p.VideoPassthrough = false
	p.VideoDecoding = true
	logger.Infow("video passthrough disabled, falling back to transcoding",
		"reason", reason)
}

func advancedHasVideoOverrides(advanced *livekit.EncodingOptions) bool {
	return advanced.VideoCodec != livekit.VideoCodec_DEFAULT_VC ||
		advanced.Width != 0 ||
		advanced.Height != 0 ||
		advanced.Depth != 0 ||
		advanced.Framerate != 0 ||
		advanced.VideoBitrate != 0 ||
		advanced.KeyFrameInterval != 0
}
