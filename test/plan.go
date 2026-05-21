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

//go:build integration

package test

import (
	"sort"
	"time"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

type EventKind int

const (
	eventPublish EventKind = iota
	eventUnpublish
	eventMute
	eventUnmute
	eventDisconnect
)

type Plan struct {
	publishers []*Publisher
}

type Publisher struct {
	name            string
	delayConnection time.Duration
	audio           []Event // sorted by pts ascending
	video           []Event // sorted by pts ascending
}

type Event struct {
	pts      time.Duration
	kind     EventKind
	codec    types.MimeType // meaningful only when kind == eventPublish
	duration time.Duration  // meaningful only when kind == eventDisconnect
}

const (
	rotationSlot = 5 * time.Second
	// transitionOffset is added to mute/unmute/unpublish/republish/
	// disconnect event PTS so transitions fall between beeps and
	// flashes (which are emitted at integer-second marks). Without
	// this, a transition at the same moment as an event causes flaky
	// detection — the partial event might or might not be picked up.
	transitionOffset = 500 * time.Millisecond
)

func planTest(tc *testCase) *Plan {
	if tc.multiParticipant {
		return planMultiParticipant(tc)
	}
	return planSingleParticipant(tc)
}

func planSingleParticipant(tc *testCase) *Plan {
	p := &Publisher{
		name: "p0",
	}

	if tc.audioCodec != "" {
		p.audio = []Event{
			{pts: tc.audioDelay, kind: eventPublish, codec: tc.audioCodec},
		}
		if tc.audioUnpublish != 0 {
			p.audio = append(p.audio, Event{
				pts: tc.audioUnpublish + transitionOffset, kind: eventUnpublish,
			})
			if tc.audioRepublish != 0 {
				p.audio = append(p.audio, Event{
					pts: tc.audioRepublish, kind: eventPublish, codec: tc.audioCodec,
				})
			}
		}
	}

	if tc.videoCodec != "" {
		p.video = []Event{
			{pts: tc.videoDelay, kind: eventPublish, codec: tc.videoCodec},
		}
		if tc.videoUnpublish != 0 {
			p.video = append(p.video, Event{
				pts: tc.videoUnpublish + transitionOffset, kind: eventUnpublish,
			})
			if tc.videoRepublish != 0 {
				p.video = append(p.video, Event{
					pts: tc.videoRepublish, kind: eventPublish, codec: tc.videoCodec,
				})
			}
		}
	}

	if tc.disconnectAt != 0 {
		discPTS := tc.disconnectAt + transitionOffset
		if len(p.audio) > 0 {
			p.audio = append(p.audio, Event{pts: discPTS, kind: eventDisconnect, duration: tc.disconnectDuration})
		}
		if len(p.video) > 0 {
			p.video = append(p.video, Event{pts: discPTS, kind: eventDisconnect, duration: tc.disconnectDuration})
		}
		// SimulateDisconnection brings the track back after duration, so
		// model the reconnect as a fresh publish for verification.
		if tc.disconnectDuration > 0 {
			reconnectPTS := discPTS + tc.disconnectDuration
			if len(p.audio) > 0 {
				p.audio = append(p.audio, Event{pts: reconnectPTS, kind: eventPublish, codec: tc.audioCodec})
			}
			if len(p.video) > 0 {
				p.video = append(p.video, Event{pts: reconnectPTS, kind: eventPublish, codec: tc.videoCodec})
			}
		}
	}

	sortEvents(p)
	return &Plan{publishers: []*Publisher{p}}
}

func planMultiParticipant(tc *testCase) *Plan {
	participants := []string{"p0", "p1", "p2"}
	rotates := tc.layout == layoutSpeaker || tc.layout == layoutSingleSpeaker

	plan := &Plan{}
	for i, name := range participants {
		p := &Publisher{name: name}

		if !tc.videoOnly && participantHasAudioInOutput(tc, name) {
			p.audio = []Event{{pts: 0, kind: eventPublish, codec: types.MimeTypeOpus}}
			if rotates {
				p.audio = append(p.audio, rotationEvents(i, len(participants), rotationSlot)...)
			}
		}
		if !tc.audioOnly && participantHasVideoInOutput(tc, name) {
			p.video = []Event{{pts: 0, kind: eventPublish, codec: types.MimeTypeH264}}
		}

		sortEvents(p)
		plan.publishers = append(plan.publishers, p)
	}
	return plan
}

// participantHasAudioInOutput reports whether pN's audio is expected in
// the encoded output. With no v2 audio routes, the legacy room-composite
// behavior applies — every participant is mixed in. With routes, only
// participants matched by at least one route appear.
func participantHasAudioInOutput(tc *testCase, name string) bool {
	if len(tc.audioRoutes) == 0 {
		return true
	}
	for _, route := range tc.audioRoutes {
		switch m := route.Match.(type) {
		case *livekit.AudioRoute_TrackId:
			if m.TrackId == setAtRuntime && name == "p0" {
				return true
			}
		case *livekit.AudioRoute_ParticipantIdentity:
			if matchesParticipantSentinel(m.ParticipantIdentity, name) {
				return true
			}
		case *livekit.AudioRoute_ParticipantKind:
			// publish.go assigns p1 = AGENT, others = STANDARD.
			isAgent := name == "p1"
			if m.ParticipantKind == livekit.ParticipantInfo_AGENT && isAgent {
				return true
			}
			if m.ParticipantKind == livekit.ParticipantInfo_STANDARD && !isAgent {
				return true
			}
		}
	}
	return false
}

// participantHasVideoInOutput reports whether pN's video is expected in
// the encoded output. Without an explicit Media video source, all
// participants contribute (room-composite layout); with one, only the
// matched participant does.
func participantHasVideoInOutput(tc *testCase, name string) bool {
	if tc.mediaVideoTrackID == "" && tc.mediaParticipantVideo == nil {
		return true
	}
	// mediaVideoTrackID always resolves to p0's video track in the harness.
	if tc.mediaVideoTrackID != "" && name == "p0" {
		return true
	}
	if tc.mediaParticipantVideo != nil &&
		matchesParticipantSentinel(tc.mediaParticipantVideo.Identity, name) {
		return true
	}
	return false
}

func matchesParticipantSentinel(sentinel, name string) bool {
	switch sentinel {
	case setAtRuntime:
		return name == "p0"
	case setP1Identity:
		return name == "p1"
	case setP2Identity:
		return name == "p2"
	}
	return false
}

func sortEvents(p *Publisher) {
	sort.SliceStable(p.audio, func(i, j int) bool { return p.audio[i].pts < p.audio[j].pts })
	sort.SliceStable(p.video, func(i, j int) bool { return p.video[i].pts < p.video[j].pts })
}

// rotationEvents creates an N-way active-speaker rotation using muting.
// Mute / unmute events are emitted at slot boundaries + transitionOffset
// so the transition lands between 1Hz beeps rather than racing one.
func rotationEvents(idx, n int, slot time.Duration) []Event {
	const maxCycles = 60
	cycle := time.Duration(n) * slot
	var events []Event

	// Non-leader participants start muted. Offset the initial mute so it
	// fires after the publish has settled — racing publish@0 with mute@0
	// in the same goroutine sometimes leaves chrome subscribers in a
	// state where the egress never transitions to ACTIVE (manifests as
	// "subscriber requested backup codec but no track found" warnings).
	if idx > 0 {
		events = append(events, Event{pts: transitionOffset, kind: eventMute})
	}

	for c := 0; c < maxCycles; c++ {
		cycleStart := time.Duration(c) * cycle
		mySlotStart := cycleStart + time.Duration(idx)*slot
		mySlotEnd := mySlotStart + slot

		// p0 in cycle 0 is already unmuted by default.
		if c != 0 || idx != 0 {
			events = append(events, Event{pts: mySlotStart + transitionOffset, kind: eventUnmute})
		}
		events = append(events, Event{pts: mySlotEnd + transitionOffset, kind: eventMute})
	}
	return events
}

// --- plan query methods -----------------------------------------------

type verdict int

const (
	forbidden verdict = iota
	required
	optional
)

const (
	publishSettling   = 3 * time.Second // bandpass / decoder warmup after publish; also applied as recording-warmup window (extends until intLag+publishSettling, see verifyContent)
	videoKeyframeWait = 2 * time.Second // keyframe wait after video unmute
	muteEdgeGrace     = 1 * time.Second // ± window around audio mute/unmute
	unpublishGrace    = 1 * time.Second // ± window around publisher unpublish / disconnect
	stageSwitchGrace  = 2 * time.Second // stage may still show prior speaker
)

// expectedBeepsBySec projects the plan timeline into per-integer-second
// lists of participant names that may beep at that plan time — both
// required slots and optional (grace) slots. Used as the alignment
// target for lag detection: optional slots are part of the publisher's
// footprint and must not be treated as "no content" or cadence.IntegerLag
// will shift past them.
func (pl *Plan) expectedBeepsBySec(end time.Duration) [][]string {
	maxSec := int64(end/time.Second) + 1
	out := make([][]string, maxSec)
	for s := int64(0); s < maxSec; s++ {
		t := time.Duration(s) * time.Second
		for _, p := range pl.publishers {
			if p.expectsBeep(t) != forbidden {
				out[s] = append(out[s], p.name)
			}
		}
	}
	return out
}

// activeSpeaker returns the publisher whose audio is unmuted at t, or
// "" when within stageSwitchGrace of an audio mute/unmute on any
// publisher (stage rendering may not have switched yet).
func (pl *Plan) activeSpeaker(t time.Duration) string {
	// In a transition window? Skip the check.
	for _, p := range pl.publishers {
		for _, e := range p.audio {
			if e.pts > t {
				break
			}
			if (e.kind == eventMute || e.kind == eventUnmute) &&
				e.pts > t-stageSwitchGrace {
				return ""
			}
		}
	}
	for _, p := range pl.publishers {
		if p.expectsBeep(t) == required {
			return p.name
		}
	}
	return ""
}

// expectsBeep returns the verdict for whether a beep should be heard
// from this publisher at time t. Grace bands collapsed:
//   - within publishSettling after audio publish → optional
//   - within ±muteEdgeGrace of audio mute/unmute → optional
//   - within ±unpublishGrace of unpublish/disconnect → optional
//   - otherwise base state (published+unmuted → required, else forbidden)
func (p *Publisher) expectsBeep(t time.Duration) verdict {
	published, unmuted := false, false
	publishGraceActive := false
	muteEdgeActive := false
	unpublishGraceActive := false
	for _, e := range p.audio {
		if e.pts > t+muteEdgeGrace {
			break
		}
		if e.pts <= t {
			switch e.kind {
			case eventPublish:
				published, unmuted = true, true
				if e.pts > t-publishSettling {
					publishGraceActive = true
				}
			case eventUnpublish, eventDisconnect:
				// Disconnect ends the track like unpublish for verification.
				published = false
				if e.pts > t-unpublishGrace {
					unpublishGraceActive = true
				}
			case eventMute:
				unmuted = false
				if e.pts > t-muteEdgeGrace {
					muteEdgeActive = true
				}
			case eventUnmute:
				unmuted = true
				if e.pts > t-muteEdgeGrace {
					muteEdgeActive = true
				}
			}
		} else {
			// future event (within mute-edge / unpublish lookahead)
			switch e.kind {
			case eventMute, eventUnmute:
				muteEdgeActive = true
			case eventUnpublish, eventDisconnect:
				unpublishGraceActive = true
			case eventPublish:
				// Imminent (re-)publish: bucket may straddle the publish boundary.
				publishGraceActive = true
			}
		}
	}
	if publishGraceActive || muteEdgeActive || unpublishGraceActive {
		return optional
	}
	if published && unmuted {
		return required
	}
	return forbidden
}

// expectsFlash returns the verdict for whether a flash should be seen
// from this publisher at time t. Grace bands:
//   - within publishSettling after video publish → optional
//   - within videoKeyframeWait after video unmute → optional
//   - within ±muteEdgeGrace of video mute → optional (leak tolerance)
//   - within ±unpublishGrace of unpublish/disconnect → optional
func (p *Publisher) expectsFlash(t time.Duration) verdict {
	published := false
	publishGraceActive := false
	unmuteGraceActive := false
	muteEdgeActive := false
	unpublishGraceActive := false
	for _, e := range p.video {
		if e.pts > t+muteEdgeGrace {
			break
		}
		if e.pts <= t {
			switch e.kind {
			case eventPublish:
				published = true
				if e.pts > t-publishSettling {
					publishGraceActive = true
				}
			case eventUnpublish, eventDisconnect:
				// Disconnect ends the track like unpublish for verification.
				published = false
				if e.pts > t-unpublishGrace {
					unpublishGraceActive = true
				}
			case eventUnmute:
				if e.pts > t-videoKeyframeWait {
					unmuteGraceActive = true
				}
			case eventMute:
				if e.pts > t-muteEdgeGrace {
					muteEdgeActive = true
				}
			}
		} else {
			switch e.kind {
			case eventMute:
				muteEdgeActive = true
			case eventUnpublish, eventDisconnect:
				unpublishGraceActive = true
			case eventPublish:
				publishGraceActive = true
			}
		}
	}
	if publishGraceActive || unmuteGraceActive || muteEdgeActive || unpublishGraceActive {
		return optional
	}
	if published {
		return required
	}
	return forbidden
}
