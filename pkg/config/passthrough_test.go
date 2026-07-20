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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"

	"github.com/livekit/egress/pkg/types"
)

func newPassthroughTestConfig(enablePassthrough bool) *PipelineConfig {
	return &PipelineConfig{
		BaseConfig: BaseConfig{
			Logging:                &logger.Config{Level: "info"},
			TemplateBase:           "http://localhost:7980/",
			EnableVideoPassthrough: enablePassthrough,
		},
		Outputs: make(map[types.EgressType][]OutputConfig),
		Live:    true,
	}
}

func segmentOutputs() []*livekit.SegmentedFileOutput {
	return []*livekit.SegmentedFileOutput{{
		FilenamePrefix: "test",
		PlaylistName:   "playlist",
	}}
}

func trackCompositeRequest(opts func(*livekit.TrackCompositeEgressRequest)) *rpc.StartEgressRequest {
	tc := &livekit.TrackCompositeEgressRequest{
		RoomName:       "room",
		AudioTrackId:   "TR_audio",
		VideoTrackId:   "TR_video",
		SegmentOutputs: segmentOutputs(),
	}
	if opts != nil {
		opts(tc)
	}
	return &rpc.StartEgressRequest{
		EgressId: "EG_test",
		Token:    "token",
		WsUrl:    "wss://localhost:7880",
		Request: &rpc.StartEgressRequest_TrackComposite{
			TrackComposite: tc,
		},
	}
}

func TestVideoPassthroughEnabled(t *testing.T) {
	p := newPassthroughTestConfig(true)
	require.NoError(t, p.Update(trackCompositeRequest(nil)))

	require.True(t, p.VideoPassthrough)
	require.False(t, p.VideoDecoding)
	require.False(t, p.VideoEncoding)
	// HLS output still selects H264 as the target codec for the runtime check
	require.Equal(t, types.MimeTypeH264, p.VideoOutCodec)
}

func TestVideoPassthroughParticipant(t *testing.T) {
	p := newPassthroughTestConfig(true)
	req := &rpc.StartEgressRequest{
		EgressId: "EG_test",
		Token:    "token",
		WsUrl:    "wss://localhost:7880",
		Request: &rpc.StartEgressRequest_Participant{
			Participant: &livekit.ParticipantEgressRequest{
				RoomName:       "room",
				Identity:       "publisher",
				SegmentOutputs: segmentOutputs(),
			},
		},
	}
	require.NoError(t, p.Update(req))

	require.True(t, p.VideoPassthrough)
	require.False(t, p.VideoDecoding)
	require.False(t, p.VideoEncoding)
}

func TestVideoPassthroughFlagDisabled(t *testing.T) {
	p := newPassthroughTestConfig(false)
	require.NoError(t, p.Update(trackCompositeRequest(nil)))

	// identical to pre-change behavior
	require.False(t, p.VideoPassthrough)
	require.True(t, p.VideoDecoding)
	require.True(t, p.VideoEncoding)
}

func TestVideoPassthroughRoomCompositeFallback(t *testing.T) {
	p := newPassthroughTestConfig(true)
	req := &rpc.StartEgressRequest{
		EgressId: "EG_test",
		Token:    "token",
		WsUrl:    "wss://localhost:7880",
		Request: &rpc.StartEgressRequest_RoomComposite{
			RoomComposite: &livekit.RoomCompositeEgressRequest{
				RoomName:       "room",
				SegmentOutputs: segmentOutputs(),
			},
		},
	}
	require.NoError(t, p.Update(req))

	require.False(t, p.VideoPassthrough)
	require.True(t, p.VideoDecoding)
	require.True(t, p.VideoEncoding)
}

func TestVideoPassthroughPresetFallback(t *testing.T) {
	p := newPassthroughTestConfig(true)
	req := trackCompositeRequest(func(tc *livekit.TrackCompositeEgressRequest) {
		tc.Options = &livekit.TrackCompositeEgressRequest_Preset{
			Preset: livekit.EncodingOptionsPreset_H264_720P_30,
		}
	})
	require.NoError(t, p.Update(req))

	require.False(t, p.VideoPassthrough)
	require.True(t, p.VideoDecoding)
	require.True(t, p.VideoEncoding)
}

func TestVideoPassthroughAdvancedVideoOptionsFallback(t *testing.T) {
	p := newPassthroughTestConfig(true)
	req := trackCompositeRequest(func(tc *livekit.TrackCompositeEgressRequest) {
		tc.Options = &livekit.TrackCompositeEgressRequest_Advanced{
			Advanced: &livekit.EncodingOptions{
				Width:  1920,
				Height: 1080,
			},
		}
	})
	require.NoError(t, p.Update(req))

	require.False(t, p.VideoPassthrough)
	require.True(t, p.VideoDecoding)
	require.True(t, p.VideoEncoding)
}

func TestVideoPassthroughAudioOnlyAdvancedOptionsAllowed(t *testing.T) {
	p := newPassthroughTestConfig(true)
	req := trackCompositeRequest(func(tc *livekit.TrackCompositeEgressRequest) {
		tc.Options = &livekit.TrackCompositeEgressRequest_Advanced{
			Advanced: &livekit.EncodingOptions{
				AudioBitrate: 96,
			},
		}
	})
	require.NoError(t, p.Update(req))

	require.True(t, p.VideoPassthrough)
	require.False(t, p.VideoDecoding)
	require.False(t, p.VideoEncoding)
}

func TestVideoPassthroughFileOutputFallback(t *testing.T) {
	p := newPassthroughTestConfig(true)
	req := trackCompositeRequest(func(tc *livekit.TrackCompositeEgressRequest) {
		tc.SegmentOutputs = nil
		tc.FileOutputs = []*livekit.EncodedFileOutput{{
			Filepath: "test.mp4",
		}}
	})
	require.NoError(t, p.Update(req))

	require.False(t, p.VideoPassthrough)
	require.True(t, p.VideoDecoding)
	require.True(t, p.VideoEncoding)
}

func TestVideoPassthroughMixedOutputsFallback(t *testing.T) {
	p := newPassthroughTestConfig(true)
	req := trackCompositeRequest(func(tc *livekit.TrackCompositeEgressRequest) {
		tc.StreamOutputs = []*livekit.StreamOutput{{
			Protocol: livekit.StreamProtocol_RTMP,
			Urls:     []string{"rtmp://localhost/live/stream"},
		}}
	})
	require.NoError(t, p.Update(req))

	// segments + stream: stream requires an encoder, no passthrough
	require.False(t, p.VideoPassthrough)
	require.True(t, p.VideoDecoding)
	require.True(t, p.VideoEncoding)
}

func TestVideoPassthroughImageOutputFallback(t *testing.T) {
	p := newPassthroughTestConfig(true)
	req := trackCompositeRequest(func(tc *livekit.TrackCompositeEgressRequest) {
		tc.ImageOutputs = []*livekit.ImageOutput{{
			CaptureInterval: 5,
			FilenamePrefix:  "image",
		}}
	})
	require.NoError(t, p.Update(req))

	// images are parsed after segments; encoder must still be re-enabled
	require.False(t, p.VideoPassthrough)
	require.True(t, p.VideoDecoding)
	require.True(t, p.VideoEncoding)
}

func TestVideoPassthroughRuntimeCodecFallback(t *testing.T) {
	p := newPassthroughTestConfig(true)
	require.NoError(t, p.Update(trackCompositeRequest(nil)))
	require.True(t, p.VideoPassthrough)

	// equivalent to the subscription-time check in updatePreInitStateLocked
	p.DisableVideoPassthrough("input codec video/vp8 does not match output codec video/h264")

	require.False(t, p.VideoPassthrough)
	require.True(t, p.VideoDecoding)
}
