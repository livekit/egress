// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package source

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func TestTrackSelectorMatchesValues(t *testing.T) {
	tests := []struct {
		name     string
		selector config.TrackSelectorConfig
		expected bool
	}{
		{name: "track id", selector: config.TrackSelectorConfig{TrackID: "TR_camera"}, expected: true},
		{name: "participant", selector: config.TrackSelectorConfig{ParticipantIdentity: "robot-a"}, expected: true},
		{name: "track name", selector: config.TrackSelectorConfig{TrackName: "camera"}, expected: true},
		{name: "track name is exact", selector: config.TrackSelectorConfig{TrackName: "Camera"}, expected: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expected, trackSelectorMatchesValues(&test.selector, "TR_camera", "camera", "robot-a"))
		})
	}
}

func TestAudioRouteMatchesValues(t *testing.T) {
	kind := lksdk.ParticipantKind(livekit.ParticipantInfo_AGENT)
	tests := []struct {
		name     string
		route    config.AudioRouteConfig
		expected bool
	}{
		{name: "track name", route: config.AudioRouteConfig{Match: config.AudioRouteMatch{TrackName: "microphone"}}, expected: true},
		{name: "participant kind", route: config.AudioRouteConfig{Match: config.AudioRouteMatch{ParticipantKind: &kind}}, expected: true},
		{name: "different participant", route: config.AudioRouteConfig{Match: config.AudioRouteMatch{ParticipantIdentity: "robot-b"}}, expected: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expected, audioRouteMatchesValues(&test.route, "TR_mic", "microphone", "robot-a", kind))
		})
	}
}

func TestVideoSelectorPrecedence(t *testing.T) {
	selectors := []config.TrackSelectorConfig{
		{ParticipantIdentity: "robot-a"},
		{TrackName: "camera-debug", Exclude: true},
	}
	require.True(t, matchesVideoSelectors(selectors, false, "TR_main", "camera-main", "robot-a"))
	require.False(t, matchesVideoSelectors(selectors, false, "TR_debug", "camera-debug", "robot-a"))
	require.False(t, matchesVideoSelectors(selectors, false, "TR_other", "camera-main", "robot-b"))
	require.True(t, matchesVideoSelectors(selectors[1:], true, "TR_main", "camera-main", "robot-b"))
}

func TestAudioExcludeOverridesCaptureAllAndPositiveRoute(t *testing.T) {
	routes := []config.AudioRouteConfig{
		{Match: config.AudioRouteMatch{ParticipantIdentity: "robot-a"}},
		{Match: config.AudioRouteMatch{TrackName: "microphone-debug"}, Exclude: true},
	}
	kind := lksdk.ParticipantKind(livekit.ParticipantInfo_STANDARD)
	require.NotNil(t, matchingAudioRoute(routes, false, "TR_main", "microphone", "robot-a", kind))
	require.Nil(t, matchingAudioRoute(routes, true, "TR_debug", "microphone-debug", "robot-a", kind))
	require.NotNil(t, matchingAudioRoute(routes[1:], true, "TR_other", "microphone", "robot-b", kind))
}
