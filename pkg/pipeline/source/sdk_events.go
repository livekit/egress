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

package source

import (
	"time"

	"github.com/linkdata/deadlock"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// roomEventEmitter turns SDK callbacks into an ordered, deduplicated room event stream
type roomEventEmitter struct {
	mu           deadlock.Mutex
	emit         func(types.RoomEvent)
	participants map[string]roomParticipant // identity -> latest info, so a reconnect with a new SID is not a new join
	tracks       map[string]emittedTrack    // track SID -> published track
}

type roomParticipant struct {
	sid, identity, name string
}

type emittedTrack struct {
	participant roomParticipant
	info        *livekit.TrackInfo
}

func newRoomEventEmitter(emit func(types.RoomEvent)) *roomEventEmitter {
	return &roomEventEmitter{
		emit:         emit,
		participants: make(map[string]roomParticipant),
		tracks:       make(map[string]emittedTrack),
	}
}

func participantOf(p lksdk.Participant) roomParticipant {
	return roomParticipant{sid: p.SID(), identity: p.Identity(), name: p.Name()}
}

func (e *roomEventEmitter) participantJoined(p roomParticipant) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.join(p)
}

// the SDK dispatches OnParticipantConnected on a goroutine, so a track can arrive first and join on its behalf
func (e *roomEventEmitter) join(p roomParticipant) {
	_, known := e.participants[p.identity]
	e.participants[p.identity] = p
	if !known {
		e.send(types.RoomEventParticipantJoined, p, nil, livekit.DisconnectReason_UNKNOWN_REASON)
	}
}

func (e *roomEventEmitter) identities() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	identities := make([]string, 0, len(e.participants))
	for identity := range e.participants {
		identities = append(identities, identity)
	}
	return identities
}

// sync reconciles an identity with the room's current session for it, which is nil once the identity has left
func (e *roomEventEmitter) sync(identity string, current *roomParticipant, liveTracks map[string]struct{}, reason livekit.DisconnectReason) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// the SDK never fires OnTrackUnpublished on disconnect
	for sid, t := range e.tracks {
		if _, live := liveTracks[sid]; !live && t.participant.identity == identity {
			e.send(types.RoomEventTrackUnpublished, t.participant, t.info, livekit.DisconnectReason_UNKNOWN_REASON)
			delete(e.tracks, sid)
		}
	}

	p, known := e.participants[identity]
	switch {
	case !known:
	case current != nil:
		e.participants[identity] = *current
	default:
		delete(e.participants, identity)
		e.send(types.RoomEventParticipantLeft, p, nil, reason)
	}
}

func (e *roomEventEmitter) trackPublished(p roomParticipant, ti *livekit.TrackInfo, muted bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.join(p)
	if _, ok := e.tracks[ti.Sid]; ok {
		return
	}
	e.tracks[ti.Sid] = emittedTrack{participant: p, info: ti}
	e.send(types.RoomEventTrackPublished, p, ti, livekit.DisconnectReason_UNKNOWN_REASON)
	if muted {
		e.send(types.RoomEventTrackMuted, p, ti, livekit.DisconnectReason_UNKNOWN_REASON)
	}
}

func (e *roomEventEmitter) trackUnpublished(trackSID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	t, ok := e.tracks[trackSID]
	if !ok {
		return
	}
	delete(e.tracks, trackSID)
	e.send(types.RoomEventTrackUnpublished, t.participant, t.info, livekit.DisconnectReason_UNKNOWN_REASON)
}

func (e *roomEventEmitter) trackMuted(trackSID string, muted bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	t, ok := e.tracks[trackSID]
	if !ok {
		return
	}
	typ := types.RoomEventTrackUnmuted
	if muted {
		typ = types.RoomEventTrackMuted
	}
	e.send(typ, t.participant, t.info, livekit.DisconnectReason_UNKNOWN_REASON)
}

func (e *roomEventEmitter) send(typ types.RoomEventType, p roomParticipant, ti *livekit.TrackInfo, reason livekit.DisconnectReason) {
	e.emit(types.RoomEvent{
		Type:                typ,
		Time:                time.Now(),
		ParticipantSID:      p.sid,
		ParticipantIdentity: p.identity,
		ParticipantName:     p.name,
		DisconnectReason:    reason,
		Track:               ti,
	})
}
