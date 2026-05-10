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
)

type eventType int

const (
	eventTypePublish eventType = iota
	eventTypeMute
	eventTypeUnmute
	eventTypeUnpublish
	eventTypeDisconnect
)

type event struct {
	pts       time.Duration
	eventType eventType
	codec     types.MimeType
	duration  time.Duration
}

type publisherPlan struct {
	participant     string
	delayConnection time.Duration
	events          []event
	audioEvents     []event
	videoEvents     []event
}
type publishPlan struct {
	publishers []*publisherPlan
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

func planTest(tc *testCase) *publishPlan {
	if tc.multiParticipant {
		return planMultiParticipant(tc)
	}
	return planSingleParticipant(tc)
}

func planSingleParticipant(tc *testCase) *publishPlan {
	p := &publisherPlan{
		participant: "p0",
	}

	if tc.audioCodec != "" {
		p.audioEvents = []event{
			{pts: tc.audioDelay, eventType: eventTypePublish, codec: tc.audioCodec},
		}
		if tc.audioUnpublish != 0 {
			p.audioEvents = append(p.audioEvents, event{
				pts: tc.audioUnpublish + transitionOffset, eventType: eventTypeUnpublish,
			})
			if tc.audioRepublish != 0 {
				p.audioEvents = append(p.audioEvents, event{
					pts: tc.audioRepublish + transitionOffset, eventType: eventTypePublish, codec: tc.audioCodec,
				})
			}
		}
	}

	if tc.videoCodec != "" {
		p.videoEvents = []event{
			{pts: tc.videoDelay, eventType: eventTypePublish, codec: tc.videoCodec},
		}
		if tc.videoUnpublish != 0 {
			p.videoEvents = append(p.videoEvents, event{
				pts: tc.videoUnpublish + transitionOffset, eventType: eventTypeUnpublish,
			})
			if tc.videoRepublish != 0 {
				p.videoEvents = append(p.videoEvents, event{
					pts: tc.videoRepublish + transitionOffset, eventType: eventTypePublish, codec: tc.videoCodec,
				})
			}
		}
	}

	if tc.disconnectAt != 0 {
		p.events = append(p.events, event{
			pts:       tc.disconnectAt + transitionOffset,
			eventType: eventTypeDisconnect,
			duration:  tc.disconnectDuration,
		})
	}

	return &publishPlan{
		publishers: []*publisherPlan{p},
	}
}

func planMultiParticipant(tc *testCase) *publishPlan {
	participants := []string{"p0", "p1", "p2"}
	rotates := tc.layout == layoutSpeaker || tc.layout == layoutSingleSpeaker

	plan := &publishPlan{}
	for i, name := range participants {
		p := &publisherPlan{
			participant: name,
			audioEvents: []event{
				{pts: 0, eventType: eventTypePublish, codec: types.MimeTypeOpus},
			},
			videoEvents: []event{
				{pts: 0, eventType: eventTypePublish, codec: types.MimeTypeH264},
			},
		}
		if rotates {
			p.audioEvents = append(p.audioEvents, rotationEvents(i, len(participants), rotationSlot)...)
		}
		plan.publishers = append(plan.publishers, p)
	}
	return plan
}

// audioWindows returns the union of all publishers' audio windows
// clipped to [0, end).
func (p *publishPlan) audioWindows(end time.Duration) []timeWindow {
	return p.unionWindows(trackAudio, end)
}

// videoWindows returns the union of all publishers' video windows
// clipped to [0, end).
func (p *publishPlan) videoWindows(end time.Duration) []timeWindow {
	return p.unionWindows(trackVideo, end)
}

// audioWindowsFor returns the named publisher's audio windows.
func (p *publishPlan) audioWindowsFor(participant string, end time.Duration) []timeWindow {
	if pp := p.publisher(participant); pp != nil {
		return pp.windowsFor(trackAudio, end)
	}
	return nil
}

// videoWindowsFor returns the named publisher's video windows.
func (p *publishPlan) videoWindowsFor(participant string, end time.Duration) []timeWindow {
	if pp := p.publisher(participant); pp != nil {
		return pp.windowsFor(trackVideo, end)
	}
	return nil
}

func (p *publishPlan) publisher(name string) *publisherPlan {
	for _, pp := range p.publishers {
		if pp.participant == name {
			return pp
		}
	}
	return nil
}

func (p *publishPlan) unionWindows(track trackKind, end time.Duration) []timeWindow {
	var all []timeWindow
	for _, pp := range p.publishers {
		all = append(all, pp.windowsFor(track, end)...)
	}
	return mergeWindows(all)
}

// windowsFor walks track and participant disconnect events to produce
// the [start, end) intervals where the track is published, unmuted, and
// the participant connected. Open intervals at the end are clipped to
// `end`. Events past `end` are ignored.
func (pp *publisherPlan) windowsFor(track trackKind, end time.Duration) []timeWindow {
	var trackEvents []event
	switch track {
	case trackAudio:
		trackEvents = pp.audioEvents
	case trackVideo:
		trackEvents = pp.videoEvents
	}

	type delta struct {
		pts                            time.Duration
		published, muted, disconnected int8
	}
	var stream []delta

	for _, e := range trackEvents {
		switch e.eventType {
		case eventTypePublish:
			stream = append(stream, delta{pts: e.pts, published: +1})
		case eventTypeUnpublish:
			stream = append(stream, delta{pts: e.pts, published: -1})
		case eventTypeMute:
			stream = append(stream, delta{pts: e.pts, muted: +1})
		case eventTypeUnmute:
			stream = append(stream, delta{pts: e.pts, muted: -1})
		}
	}
	for _, e := range pp.events {
		if e.eventType != eventTypeDisconnect {
			continue
		}
		stream = append(stream, delta{pts: e.pts, disconnected: +1})
		if e.duration > 0 {
			stream = append(stream, delta{pts: e.pts + e.duration, disconnected: -1})
		}
	}
	sort.SliceStable(stream, func(i, j int) bool { return stream[i].pts < stream[j].pts })

	var windows []timeWindow
	var open bool
	var openSince time.Duration
	var published, muted, disconnected int

	flush := func(at time.Duration) {
		want := published > 0 && muted == 0 && disconnected == 0
		if want == open {
			return
		}
		if want {
			openSince = at
		} else if at > openSince {
			windows = append(windows, timeWindow{openSince, at})
		}
		open = want
	}

	for _, d := range stream {
		if d.pts > end {
			break
		}
		published += int(d.published)
		muted += int(d.muted)
		disconnected += int(d.disconnected)
		flush(d.pts)
	}
	if open && end > openSince {
		windows = append(windows, timeWindow{openSince, end})
	}
	return windows
}

// mergeWindows sorts and unions an arbitrary set of windows.
func mergeWindows(in []timeWindow) []timeWindow {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool { return in[i].start < in[j].start })
	out := []timeWindow{in[0]}
	for _, w := range in[1:] {
		last := &out[len(out)-1]
		if w.start <= last.end {
			if w.end > last.end {
				last.end = w.end
			}
		} else {
			out = append(out, w)
		}
	}
	return out
}

// rotationEvents creates an N-way active-speaker rotation using muting.
// Mute / unmute events are emitted at slot boundaries + transitionOffset
// so the transition lands between 1Hz beeps rather than racing one.
func rotationEvents(idx, n int, slot time.Duration) []event {
	const maxCycles = 60
	cycle := time.Duration(n) * slot
	var events []event

	// Non-leader participants start muted. Offset the initial mute so it
	// fires after the publish has settled — racing publish@0 with mute@0
	// in the same goroutine sometimes leaves chrome subscribers in a
	// state where the egress never transitions to ACTIVE (manifests as
	// "subscriber requested backup codec but no track found" warnings).
	if idx > 0 {
		events = append(events, event{pts: transitionOffset, eventType: eventTypeMute})
	}

	for c := 0; c < maxCycles; c++ {
		cycleStart := time.Duration(c) * cycle
		mySlotStart := cycleStart + time.Duration(idx)*slot
		mySlotEnd := mySlotStart + slot

		// p0 in cycle 0 is already unmuted by default.
		if c != 0 || idx != 0 {
			events = append(events, event{pts: mySlotStart + transitionOffset, eventType: eventTypeUnmute})
		}
		events = append(events, event{pts: mySlotEnd + transitionOffset, eventType: eventTypeMute})
	}
	return events
}
