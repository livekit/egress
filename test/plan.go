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

// interval is a half-open [start, end) range.
type interval struct {
	start, end time.Duration
}

type gapMethod int

const (
	gapMute gapMethod = iota
	gapUnpublishRepublish
	gapDisconnect
)

type gap struct {
	start, end time.Duration
	method     gapMethod
}

type streamPlan struct {
	participant string
	codec       types.MimeType
	publish     time.Duration
	// unpublish=0 means run until the test ends.
	unpublish time.Duration
	gaps      []gap
}

type publishPlan struct {
	audio []*streamPlan
	video []*streamPlan
}

const rotationSlot = 5 * time.Second

func planTest(tc *testCase) *publishPlan {
	if tc.multiParticipant {
		return planMultiParticipant(tc)
	}
	return planSingleParticipant(tc)
}

func planSingleParticipant(tc *testCase) *publishPlan {
	p := &publishPlan{}

	if tc.audioCodec != "" {
		s := &streamPlan{
			participant: "p0",
			codec:       tc.audioCodec,
			publish:     tc.audioDelay,
			unpublish:   tc.audioUnpublish,
		}
		// audioRepublish > 0 means audioUnpublish opens a gap, not a final teardown.
		if tc.audioRepublish > 0 {
			s.unpublish = 0
			s.gaps = append(s.gaps, gap{
				start:  tc.audioUnpublish,
				end:    tc.audioRepublish,
				method: gapUnpublishRepublish,
			})
		}
		p.audio = append(p.audio, s)
	}

	if tc.videoCodec != "" {
		s := &streamPlan{
			participant: "p0",
			codec:       tc.videoCodec,
			publish:     tc.videoDelay,
			unpublish:   tc.videoUnpublish,
		}
		if tc.videoRepublish > 0 {
			s.unpublish = 0
			s.gaps = append(s.gaps, gap{
				start:  tc.videoUnpublish,
				end:    tc.videoRepublish,
				method: gapUnpublishRepublish,
			})
		}
		p.video = append(p.video, s)
	}

	// Disconnect is participant-wide: same gap on every track.
	if tc.disconnectDuration > 0 {
		dgap := gap{
			start:  tc.disconnectAt,
			end:    tc.disconnectAt + tc.disconnectDuration,
			method: gapDisconnect,
		}
		for _, s := range p.audio {
			s.gaps = append(s.gaps, dgap)
		}
		for _, s := range p.video {
			s.gaps = append(s.gaps, dgap)
		}
	}

	for _, s := range p.audio {
		sortGaps(s.gaps)
	}
	for _, s := range p.video {
		sortGaps(s.gaps)
	}
	return p
}

func planMultiParticipant(tc *testCase) *publishPlan {
	p := &publishPlan{}
	participants := []string{"p0", "p1", "p2"}
	rotates := tc.layout == layoutSpeaker || tc.layout == layoutSingleSpeaker

	for i, name := range participants {
		audio := &streamPlan{participant: name, codec: types.MimeTypeOpus}
		video := &streamPlan{participant: name, codec: types.MimeTypeH264}
		if rotates {
			audio.gaps = rotationGaps(i, len(participants), rotationSlot)
		}
		p.audio = append(p.audio, audio)
		p.video = append(p.video, video)
	}
	return p
}

// rotationGaps mutes participant idx outside its slot in an N-way round
// robin: unmuted in [idx, idx+1), [idx+N, idx+N+1), ... A generous cycle
// count covers any realistic test duration; intervals() clips at the end.
func rotationGaps(idx, n int, slot time.Duration) []gap {
	const maxCycles = 60
	cycle := time.Duration(n) * slot
	var gaps []gap
	for c := 0; c < maxCycles; c++ {
		cycleStart := time.Duration(c) * cycle
		mySlotStart := cycleStart + time.Duration(idx)*slot
		mySlotEnd := mySlotStart + slot
		if mySlotStart > cycleStart {
			gaps = append(gaps, gap{cycleStart, mySlotStart, gapMute})
		}
		cycleEnd := cycleStart + cycle
		if mySlotEnd < cycleEnd {
			gaps = append(gaps, gap{mySlotEnd, cycleEnd, gapMute})
		}
	}
	return gaps
}

func sortGaps(gs []gap) {
	sort.Slice(gs, func(i, j int) bool { return gs[i].start < gs[j].start })
}

// intervals returns when media is flowing, clipped to [0, end]. Gaps are
// assumed sorted and non-overlapping (the planner only emits one source
// of gaps per stream — rotation, disconnect, or unpublish/republish).
func (s *streamPlan) intervals(end time.Duration) []interval {
	final := s.unpublish
	if final == 0 || final > end {
		final = end
	}
	if final <= s.publish {
		return nil
	}
	open := []interval{{s.publish, final}}
	for _, g := range s.gaps {
		if g.start >= final || g.end <= s.publish {
			continue
		}
		gs, ge := g.start, g.end
		if gs < s.publish {
			gs = s.publish
		}
		if ge > final {
			ge = final
		}
		open = subtractInterval(open, gs, ge)
	}
	return open
}

func subtractInterval(in []interval, gs, ge time.Duration) []interval {
	out := make([]interval, 0, len(in)+1)
	for _, iv := range in {
		switch {
		case ge <= iv.start || gs >= iv.end:
			out = append(out, iv)
		case gs <= iv.start && ge >= iv.end:
			// gap covers interval, drop
		case gs <= iv.start && ge < iv.end:
			out = append(out, interval{ge, iv.end})
		case gs > iv.start && ge >= iv.end:
			out = append(out, interval{iv.start, gs})
		default:
			out = append(out, interval{iv.start, gs}, interval{ge, iv.end})
		}
	}
	return out
}

func (p *publishPlan) audioWindows(end time.Duration) []timeWindow {
	return windowsFromStreams(p.audio, end)
}

func (p *publishPlan) videoWindows(end time.Duration) []timeWindow {
	return windowsFromStreams(p.video, end)
}

func (p *publishPlan) audioWindowsFor(participant string, end time.Duration) []timeWindow {
	return participantWindows(p.audio, participant, end)
}

func (p *publishPlan) videoWindowsFor(participant string, end time.Duration) []timeWindow {
	return participantWindows(p.video, participant, end)
}

func windowsFromStreams(streams []*streamPlan, end time.Duration) []timeWindow {
	var all []timeWindow
	for _, s := range streams {
		for _, iv := range s.intervals(end) {
			all = append(all, timeWindow(iv))
		}
	}
	return mergeWindows(all)
}

func participantWindows(streams []*streamPlan, participant string, end time.Duration) []timeWindow {
	for _, s := range streams {
		if s.participant == participant {
			ivs := s.intervals(end)
			out := make([]timeWindow, len(ivs))
			for i, iv := range ivs {
				out[i] = timeWindow(iv)
			}
			return out
		}
	}
	return nil
}

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
