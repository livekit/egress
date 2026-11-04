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
	lksdk "github.com/livekit/server-sdk-go/v2"
)

type TrackMatch struct {
	TrackID             string
	ParticipantIdentity string
	ParticipantKind     *lksdk.ParticipantKind
	TrackName           string
}

type TrackSelection struct {
	CaptureAll bool
	Selectors  []TrackSelector
}

type TrackSelector struct {
	Match   TrackMatch
	Exclude bool
}

// Matches reports whether every set field equals the publication's value; an empty match matches nothing
func (m TrackMatch) Matches(trackID, trackName, identity string, kind lksdk.ParticipantKind) bool {
	if m == (TrackMatch{}) {
		return false
	}
	return (m.TrackID == "" || m.TrackID == trackID) &&
		(m.ParticipantIdentity == "" || m.ParticipantIdentity == identity) &&
		(m.ParticipantKind == nil || *m.ParticipantKind == kind) &&
		(m.TrackName == "" || m.TrackName == trackName)
}

// Includes reports whether the selection captures a publication; any matching exclude wins
func (s *TrackSelection) Includes(trackID, trackName, identity string, kind lksdk.ParticipantKind) bool {
	included := s.CaptureAll
	for _, sel := range s.Selectors {
		if !sel.Match.Matches(trackID, trackName, identity, kind) {
			continue
		}
		if sel.Exclude {
			return false
		}
		included = true
	}
	return included
}

// MatchAudioRoute returns nil for an excluded publication, else the first positive route that matches, else BOTH when captureAll
func MatchAudioRoute(routes []AudioRouteConfig, captureAll bool, trackID, trackName, identity string, kind lksdk.ParticipantKind) *AudioRouteConfig {
	var matched *AudioRouteConfig
	for i := range routes {
		route := &routes[i]
		if !route.Match.Matches(trackID, trackName, identity, kind) {
			continue
		}
		if route.Exclude {
			return nil
		}
		if matched == nil {
			matched = route
		}
	}
	if matched == nil && captureAll {
		return &AudioRouteConfig{}
	}
	return matched
}
