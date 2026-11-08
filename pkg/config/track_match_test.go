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

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func TestTrackResolution(t *testing.T) {
	agent := lksdk.ParticipantKind(livekit.ParticipantInfo_AGENT)
	standard := lksdk.ParticipantKind(livekit.ParticipantInfo_STANDARD)
	left := livekit.AudioChannel_AUDIO_CHANNEL_LEFT
	right := livekit.AudioChannel_AUDIO_CHANNEL_RIGHT

	type pub struct {
		trackID, name, identity string
		kind                    lksdk.ParticipantKind
	}
	robotCam := pub{"TR_cam", "camera", "robot", standard}

	for _, test := range []struct {
		name       string
		selectors  []TrackSelector
		captureAll bool
		pub        pub
		expected   bool
		// each row also runs as audio routes: the first gets LEFT, later ones RIGHT
		channel *livekit.AudioChannel
	}{
		{
			name:      "name alone matches any participant",
			selectors: []TrackSelector{{Match: TrackMatch{TrackName: "camera"}}},
			pub:       pub{"TR_other", "camera", "other", standard},
			expected:  true,
		},
		{
			name:      "participant and name narrow together",
			selectors: []TrackSelector{{Match: TrackMatch{ParticipantIdentity: "robot", TrackName: "lidar"}}},
			pub:       robotCam,
		},
		{
			name:      "track id with a different name does not match",
			selectors: []TrackSelector{{Match: TrackMatch{TrackID: "TR_cam", TrackName: "other"}}},
			pub:       robotCam,
		},
		{
			name:      "participant kind",
			selectors: []TrackSelector{{Match: TrackMatch{ParticipantKind: &agent}}},
			pub:       pub{"TR_mic", "mic", "agent", agent},
			expected:  true,
		},
		{
			name:       "exclude beats capture all",
			selectors:  []TrackSelector{{Match: TrackMatch{TrackName: "camera"}, Exclude: true}},
			captureAll: true,
			pub:        robotCam,
		},
		{
			name: "exclude beats an earlier positive match",
			selectors: []TrackSelector{
				{Match: TrackMatch{ParticipantIdentity: "robot"}},
				{Match: TrackMatch{TrackName: "camera"}, Exclude: true},
			},
			pub: robotCam,
		},
		{
			name: "first positive route sets the channel",
			selectors: []TrackSelector{
				{Match: TrackMatch{TrackName: "camera"}},
				{Match: TrackMatch{ParticipantIdentity: "robot"}},
			},
			pub:      robotCam,
			expected: true,
			channel:  &left,
		},
		{
			name:       "empty match matches nothing and falls back to capture all",
			selectors:  []TrackSelector{{}},
			captureAll: true,
			pub:        robotCam,
			expected:   true,
		},
		{
			name:      "no match without capture all",
			selectors: []TrackSelector{{Match: TrackMatch{ParticipantIdentity: "other"}}},
			pub:       robotCam,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := test.pub
			selection := &TrackSelection{CaptureAll: test.captureAll, Selectors: test.selectors}
			require.Equal(t, test.expected, selection.Includes(p.trackID, p.name, p.identity, p.kind))

			routes := make([]AudioRouteConfig, len(test.selectors))
			for i, sel := range test.selectors {
				routes[i] = AudioRouteConfig{Match: sel.Match, Exclude: sel.Exclude, Channel: left}
				if i > 0 {
					routes[i].Channel = right
				}
			}
			route := MatchAudioRoute(routes, test.captureAll, p.trackID, p.name, p.identity, p.kind)
			require.Equal(t, test.expected, route != nil)
			if test.channel != nil {
				require.Equal(t, *test.channel, route.Channel)
			}
		})
	}
}

func TestMatchesMediaVideo(t *testing.T) {
	standard := lksdk.ParticipantKind(livekit.ParticipantInfo_STANDARD)
	camera := &livekit.TrackInfo{Sid: "TR_cam", Name: "camera", Source: livekit.TrackSource_CAMERA, MimeType: "video/H264"}
	screen := &livekit.TrackInfo{Sid: "TR_screen", Name: "screen", Source: livekit.TrackSource_SCREEN_SHARE, MimeType: "video/H264"}
	// before publish only the codecs carry a mime type
	vp8 := &livekit.TrackInfo{Sid: "TR_vp8", Name: "camera", Source: livekit.TrackSource_CAMERA,
		Codecs: []*livekit.SimulcastCodecInfo{{MimeType: "video/VP8"}}}
	mcap := map[types.EgressType][]OutputConfig{types.EgressTypeFile: {&FileConfig{outputConfig: outputConfig{OutputType: types.OutputTypeMCAP}}}}

	for _, test := range []struct {
		name     string
		conf     *PipelineConfig
		ti       *livekit.TrackInfo
		identity string
		expected bool
	}{
		{
			name:     "video track id",
			conf:     &PipelineConfig{SourceConfig: SourceConfig{SDKSourceParams: SDKSourceParams{VideoTrackID: "TR_cam"}}},
			ti:       camera,
			identity: "alice",
			expected: true,
		},
		{
			name:     "video track id does not take another track",
			conf:     &PipelineConfig{SourceConfig: SourceConfig{SDKSourceParams: SDKSourceParams{VideoTrackID: "TR_cam"}}},
			ti:       screen,
			identity: "alice",
		},
		{
			name:     "identity takes the camera",
			conf:     &PipelineConfig{SourceConfig: SourceConfig{SDKSourceParams: SDKSourceParams{Identity: "alice"}}},
			ti:       camera,
			identity: "alice",
			expected: true,
		},
		{
			name:     "identity does not take another participant's camera",
			conf:     &PipelineConfig{SourceConfig: SourceConfig{SDKSourceParams: SDKSourceParams{Identity: "alice"}}},
			ti:       camera,
			identity: "bob",
		},
		{
			name:     "identity without screen share does not take the screen share",
			conf:     &PipelineConfig{SourceConfig: SourceConfig{SDKSourceParams: SDKSourceParams{Identity: "alice"}}},
			ti:       screen,
			identity: "alice",
		},
		{
			name:     "screen share takes only the screen share",
			conf:     &PipelineConfig{SourceConfig: SourceConfig{SDKSourceParams: SDKSourceParams{Identity: "alice", ScreenShare: true}}},
			ti:       camera,
			identity: "alice",
		},
		{
			name:     "screen share",
			conf:     &PipelineConfig{SourceConfig: SourceConfig{SDKSourceParams: SDKSourceParams{Identity: "alice", ScreenShare: true}}},
			ti:       screen,
			identity: "alice",
			expected: true,
		},
		{
			name: "passthrough identity does not select video",
			conf: &PipelineConfig{
				Passthrough:  true,
				SourceConfig: SourceConfig{SDKSourceParams: SDKSourceParams{Identity: "alice"}},
			},
			ti:       camera,
			identity: "alice",
		},
		{
			name: "video selection",
			conf: &PipelineConfig{SourceConfig: SourceConfig{SDKSourceParams: SDKSourceParams{
				VideoSelection: &TrackSelection{Selectors: []TrackSelector{{Match: TrackMatch{ParticipantIdentity: "alice"}}}},
			}}},
			ti:       screen,
			identity: "alice",
			expected: true,
		},
		{
			name: "mcap skips vp8",
			conf: &PipelineConfig{
				SourceConfig: SourceConfig{SDKSourceParams: SDKSourceParams{VideoSelection: &TrackSelection{CaptureAll: true}}},
				Outputs:      mcap,
			},
			ti:       vp8,
			identity: "alice",
		},
		{
			name: "mcap takes h264",
			conf: &PipelineConfig{
				SourceConfig: SourceConfig{SDKSourceParams: SDKSourceParams{VideoSelection: &TrackSelection{CaptureAll: true}}},
				Outputs:      mcap,
			},
			ti:       camera,
			identity: "alice",
			expected: true,
		},
		{
			name:     "no video source selects nothing",
			conf:     &PipelineConfig{},
			ti:       camera,
			identity: "alice",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expected, test.conf.MatchesMediaVideo(test.ti, test.identity, standard))
		})
	}
}

func TestMatchesMedia(t *testing.T) {
	standard := lksdk.ParticipantKind(livekit.ParticipantInfo_STANDARD)
	agent := lksdk.ParticipantKind(livekit.ParticipantInfo_AGENT)
	left := livekit.AudioChannel_AUDIO_CHANNEL_LEFT
	mic := &livekit.TrackInfo{Sid: "TR_mic", Name: "microphone", Type: livekit.TrackType_AUDIO, Source: livekit.TrackSource_MICROPHONE}
	camera := &livekit.TrackInfo{Sid: "TR_cam", Name: "camera", Type: livekit.TrackType_VIDEO, Source: livekit.TrackSource_CAMERA}
	conf := &PipelineConfig{SourceConfig: SourceConfig{SDKSourceParams: SDKSourceParams{
		VideoTrackID: "TR_cam",
		AudioRoutes: []AudioRouteConfig{
			{Match: TrackMatch{ParticipantKind: &agent}, Channel: left},
			{Match: TrackMatch{ParticipantIdentity: "bob"}, Exclude: true},
		},
		CaptureAudioAll: true,
	}}}

	route := conf.MatchMediaAudio(mic, "agent", agent)
	require.NotNil(t, route)
	require.Equal(t, left, route.Channel, "the matched route sets the channel")
	route = conf.MatchMediaAudio(mic, "alice", standard)
	require.NotNil(t, route, "capture all takes an unrouted track")
	require.Equal(t, livekit.AudioChannel_AUDIO_CHANNEL_BOTH, route.Channel)
	require.Nil(t, conf.MatchMediaAudio(mic, "bob", standard), "an exclude drops the track")
	require.Nil(t, conf.MatchMediaAudio(nil, "alice", standard))

	require.True(t, conf.MatchesMedia(mic, "alice", standard))
	require.False(t, conf.MatchesMedia(mic, "bob", standard))
	require.True(t, conf.MatchesMedia(camera, "alice", standard))
	require.False(t, conf.MatchesMedia(&livekit.TrackInfo{Sid: "TR_screen", Type: livekit.TrackType_VIDEO}, "alice", standard))
	require.False(t, conf.MatchesMedia(&livekit.TrackInfo{Sid: "TR_data", Type: livekit.TrackType_DATA}, "alice", standard))
	require.False(t, conf.MatchesMedia(nil, "alice", standard))
}
