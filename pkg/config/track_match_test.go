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
