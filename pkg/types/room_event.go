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

package types

import (
	"time"

	"github.com/livekit/protocol/livekit"
)

// RoomEventType names a room event recorded alongside media
type RoomEventType string

const (
	RoomEventParticipantJoined RoomEventType = "participant_joined"
	RoomEventParticipantLeft   RoomEventType = "participant_left"
	RoomEventTrackPublished    RoomEventType = "track_published"
	RoomEventTrackUnpublished  RoomEventType = "track_unpublished"
	RoomEventTrackMuted        RoomEventType = "track_muted"
	RoomEventTrackUnmuted      RoomEventType = "track_unmuted"
)

// RoomEvent is a participant or track lifecycle event observed by the egress
type RoomEvent struct {
	Type                RoomEventType
	Time                time.Time
	ParticipantSID      string
	ParticipantIdentity string
	ParticipantName     string
	DisconnectReason    livekit.DisconnectReason
	Track               *livekit.TrackInfo // nil for participant events
}
