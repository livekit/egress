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

const rotationSlot = 5 * time.Second

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
				pts: tc.audioUnpublish, eventType: eventTypeUnpublish,
			})
			if tc.audioRepublish != 0 {
				p.audioEvents = append(p.audioEvents, event{
					pts: tc.audioRepublish, eventType: eventTypePublish, codec: tc.audioCodec,
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
				pts: tc.videoUnpublish, eventType: eventTypeUnpublish,
			})
			if tc.videoRepublish != 0 {
				p.videoEvents = append(p.videoEvents, event{
					pts: tc.videoRepublish, eventType: eventTypePublish, codec: tc.videoCodec,
				})
			}
		}
	}

	if tc.disconnectAt != 0 {
		p.events = append(p.events, event{
			pts:       tc.disconnectAt,
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
	rotates := tc.layout == "speaker" || tc.layout == "single-speaker"

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

// rotationEvents creates an N-way active-speaker rotation using muting
func rotationEvents(idx, n int, slot time.Duration) []event {
	const maxCycles = 60
	cycle := time.Duration(n) * slot
	var events []event

	// Non-leader participants start muted.
	if idx > 0 {
		events = append(events, event{pts: 0, eventType: eventTypeMute})
	}

	for c := 0; c < maxCycles; c++ {
		cycleStart := time.Duration(c) * cycle
		mySlotStart := cycleStart + time.Duration(idx)*slot
		mySlotEnd := mySlotStart + slot

		// p0 in cycle 0 is already unmuted by default.
		if c != 0 || idx != 0 {
			events = append(events, event{pts: mySlotStart, eventType: eventTypeUnmute})
		}
		events = append(events, event{pts: mySlotEnd, eventType: eventTypeMute})
	}
	return events
}
