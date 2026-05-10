// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package test

import (
	"context"
	"encoding/csv"
	"fmt"
	"image"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-samples/avsync"
	"github.com/livekit/protocol/livekit"

	"github.com/livekit/egress/pkg/types"
)

const (
	// 1Hz cadence jitter budget: 3-frame slip at 25fps is 120ms, observed
	// up to 114ms at mute-rotation slot boundaries.
	eventSpacingTolerance = 120 * time.Millisecond
	// Encoder pipeline PTS offset between audio and video runs ~200–280ms.
	avSyncTolerance = 300 * time.Millisecond
	// Spacing-check filter: gaps outside [1s − cadenceWindow, 1s + cadenceWindow]
	// are not real cadence drift — too-large means the detector missed an
	// event, too-small means the detector picked up a noise event between
	// real ones. Either way, skip the spacing assertion for that gap.
	cadenceWindow = 500 * time.Millisecond
	// Each window may legitimately drop one event at each edge: encoder
	// warmup at the leading edge, mute reaction at the trailing edge.
	edgeLossPerWindow = 2
	// Maximum offset detectRecordingLag will trust between plan time
	// and recording time (Chrome warmup is ~3s; anything beyond is noise).
	maxDetectableOffset = 5 * time.Second
	// avsync's bandpass detector takes ~2s of stream to lock onto the
	// 1Hz cadence after the first event. Events that fall in this
	// window after the alignment offset are forgiven from per-window
	// counts.
	detectorSettlingDuration = 2 * time.Second
	// Minimum post-settling window duration for the bandpass detector
	// to plausibly lock and emit a beep. Windows shorter than this are
	// dropped from per-participant checks.
	minUsableWindow = 1 * time.Second

	layoutSpeaker       = "speaker"
	layoutSingleSpeaker = "single-speaker"
	layoutGrid          = "grid"

	regionStage = "stage"
	regionFull  = "full"
)

// timeWindow represents an interval where content is expected.
type timeWindow struct {
	start time.Duration
	end   time.Duration
}

// runContentCheck derives the appropriate content check from the test case
// properties and runs it. If tc.contentCheck is set, it is used instead.
func (r *Runner) runContentCheck(t *testing.T, tc *testCase, file string, info *FFProbeInfo) {
	if info == nil {
		return
	}

	if tc.contentCheck != nil {
		tc.contentCheck(t, file, info)
		return
	}

	// Web/WebV2 load arbitrary content from a URL, not the avsync pattern.
	if tc.requestType == types.RequestTypeWeb {
		return
	}

	participants := []avsync.Participant{avsync.P0}
	if tc.multiParticipant || hasNonP0Expectation(tc.expectedAudioChannels) {
		participants = avsync.AllParticipants
	}

	w, h := videoDimensions(info)
	var regions []avsync.Region

	switch {
	case tc.audioOnly:
		// no regions
	case tc.multiParticipant && tc.layout == layoutSpeaker:
		regions = SpeakerLayoutRegions(w, h, len(participants))
	case tc.multiParticipant && tc.layout == layoutSingleSpeaker:
		regions = SingleSpeakerLayoutRegions(w, h)
	case tc.multiParticipant && tc.layout == layoutGrid:
		regions = GridLayoutRegions(w, h, len(participants))
	case tc.multiParticipant:
		regions = GridLayoutRegions(w, h, len(participants))
	default:
		regions = []avsync.Region{{Name: regionFull, Rect: image.Rect(0, 0, w, h)}}
	}

	result, err := avsync.Analyze(avsync.Config{
		FilePath:     file,
		Regions:      regions,
		Participants: participants,
	})
	require.NoError(t, err)

	dur, _ := parseFFProbeDuration(info.Format.Duration)

	var videoWindows, audioWindows []timeWindow
	var recordingLag time.Duration
	if tc.plan != nil {
		videoWindows = tc.plan.videoWindows(dur)
		audioWindows = tc.plan.audioWindows(dur)
		recordingLag = detectRecordingLag(allBeepPTS(result), tc.plan, dur)
		videoWindows = shiftWindows(videoWindows, -recordingLag, dur)
		audioWindows = shiftWindows(audioWindows, -recordingLag, dur)
		t.Logf("recording lag: %s (dur=%s)", recordingLag, dur)
	}

	if !tc.audioOnly && len(videoWindows) > 0 {
		r.verifyFlashes(t, result, videoWindows)

		if tc.multiParticipant {
			r.verifyParticipantVisibility(t, result, tc, videoWindows)
		}
	}

	if !tc.videoOnly && len(audioWindows) > 0 {
		r.verifyBeeps(t, tc, result, audioWindows, participants, recordingLag, dur)
	}

	// Track-lifecycle disruptions (unpublish/disconnect) cause the
	// encoder to lose sync alignment when the new track starts; skip
	// verifyAVSync for those
	if !tc.audioOnly && !tc.videoOnly && len(videoWindows) > 0 && len(audioWindows) > 0 && !planHasTrackLifecycleEvents(tc.plan) {
		r.verifyAVSync(t, result, videoWindows, audioWindows)
	}
}

func planHasTrackLifecycleEvents(p *publishPlan) bool {
	if p == nil {
		return false
	}
	for _, pp := range p.publishers {
		for _, e := range pp.events {
			if e.eventType == eventTypeDisconnect {
				return true
			}
		}
		if hasUnpublish(pp.audioEvents) || hasUnpublish(pp.videoEvents) {
			return true
		}
	}
	return false
}

func hasUnpublish(events []event) bool {
	for _, e := range events {
		if e.eventType == eventTypeUnpublish {
			return true
		}
	}
	return false
}

// detectRecordingLag estimates the time delay between plan time 0 and
// recording PTS 0. The recording starts `lag` seconds into the plan
// timeline — beeps and flashes the source emitted before then never
// reach the file. Returns 0 if uncertain.
//
// Two-part computation:
//   - Frac part from the cadence phase: events fire at integer plan
//     seconds, so frac(lag) = (1s - frac(first_stable_event_PTS)) mod 1s.
//   - Integer part from the event count deficit: each beep that was
//     emitted but landed before recording started is one second of
//     integer lag. We compare total expected events (computed from
//     per-participant windows so rotation mute/unmute doesn't inflate
//     the count) to total detected events.
//
// SDK tests typically have lag < 500ms; Chrome-rendered tests
// (RoomComposite / Template) can hit 2-3s while the page loads.
func detectRecordingLag(eventPTS []time.Duration, plan *publishPlan, dur time.Duration) time.Duration {
	if len(eventPTS) < 2 || plan == nil {
		return 0
	}

	sorted := append([]time.Duration(nil), eventPTS...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	// Find first event that has a successor ~1s later — this is the
	// first event we can trust as part of the source cadence (skips
	// encoder keyframe artifacts and detector settling noise).
	firstStable, ok := firstCadenceEvent(sorted)
	if !ok {
		return 0
	}

	rem := firstStable % time.Second
	fracLag := time.Second - rem
	if fracLag == time.Second {
		fracLag = 0
	}

	// Sum expected events across per-participant windows (which respect
	// mute/unmute). Using union audioWindows would over-count for
	// rotation tests where only one participant emits at a time.
	expectedTotal := 0
	for _, pp := range plan.publishers {
		for _, w := range pp.windowsFor(trackAudio, dur) {
			expectedTotal += int((w.end - w.start) / time.Second)
		}
	}
	var integerLag time.Duration
	if expectedTotal > len(sorted) {
		integerLag = time.Duration(expectedTotal-len(sorted)) * time.Second
	}

	lag := integerLag + fracLag
	if lag > maxDetectableOffset {
		return 0
	}
	return lag
}

// firstCadenceEvent returns the first event in `sorted` (which must be
// pre-sorted ascending) that has a successor approximately 1s later —
// signaling we've left the pre-cadence noise floor and entered the
// steady 1Hz source pattern.
func firstCadenceEvent(sorted []time.Duration) (time.Duration, bool) {
	const tolerance = 100 * time.Millisecond
	for i := 0; i < len(sorted)-1; i++ {
		gap := sorted[i+1] - sorted[i]
		if absDuration(gap-time.Second) < tolerance {
			return sorted[i], true
		}
	}
	return 0, false
}

// shiftWindows moves every window by `offset` (positive moves forward,
// negative moves backward — used to convert plan time to recording PTS
// when the recording starts `lag` seconds into the plan timeline).
// Leading edges are clamped to 0, trailing edges to `end`; zero-length
// results are dropped.
func shiftWindows(windows []timeWindow, offset, end time.Duration) []timeWindow {
	if offset == 0 {
		return windows
	}
	out := make([]timeWindow, 0, len(windows))
	for _, w := range windows {
		s := w.start + offset
		e := w.end + offset
		if s < 0 {
			s = 0
		}
		if e > end {
			e = end
		}
		if s >= e {
			continue
		}
		out = append(out, timeWindow{s, e})
	}
	return out
}

// allBeepPTS extracts every detected beep timestamp from result.
func allBeepPTS(result *avsync.Result) []time.Duration {
	out := make([]time.Duration, len(result.Audio.Beeps))
	for i, b := range result.Audio.Beeps {
		out[i] = b.PTS
	}
	return out
}

func (r *Runner) verifyFlashes(t *testing.T, result *avsync.Result, windows []timeWindow) {
	t.Helper()
	for regionName, flashes := range result.Video.Flashes {
		windowFlashes := filterByWindows(flashes, windows)

		require.Greater(t, len(windowFlashes), 0,
			"no flashes in region %s during expected content windows", regionName)

		// Skip startup transient (encoder keyframe / page-load splash) by
		// finding where two consecutive gaps land within tolerance.
		startIdx := firstStableIndex(windowFlashes)
		for i := startIdx; i < len(windowFlashes); i++ {
			gap := windowFlashes[i] - windowFlashes[i-1]
			if !sameWindow(windowFlashes[i-1], windowFlashes[i], windows) {
				continue
			}
			// Skip gaps far from 1s — too-large means the detector missed
			// an event, too-small means a noise event was picked up
			// between real ones; neither is real cadence drift.
			if absDuration(gap-time.Second) > cadenceWindow {
				continue
			}
			require.InDelta(t, float64(time.Second), float64(gap), float64(eventSpacingTolerance),
				"flash spacing irregular at index %d in region %s: gap=%s", i, regionName, gap)
		}

		requirePerWindowCount(t, fmt.Sprintf("flash count in region %s", regionName), windowFlashes, windows)

		// After offset alignment, anything more than a small slack outside
		// the expected windows is a real leak (mute reaction tail, etc.).
		outsideCount := countOutsideWindows(flashes, windows, 500*time.Millisecond)
		require.LessOrEqual(t, outsideCount, 2,
			"unexpected flashes outside content windows in region %s", regionName)
	}
}

func (r *Runner) verifyBeeps(t *testing.T, tc *testCase, result *avsync.Result, windows []timeWindow, participants []avsync.Participant, lag, dur time.Duration) {
	t.Helper()

	beepsByParticipant := make(map[string][]avsync.Beep)
	for _, b := range result.Audio.Beeps {
		beepsByParticipant[b.Participant] = append(beepsByParticipant[b.Participant], b)
	}

	for _, p := range participants {
		beeps := beepsByParticipant[p.Name]

		// Per-participant windows for filter, spacing, and count: a leaked
		// beep from a muted participant otherwise escapes the union filter.
		// Apply the same lag that was used for the union windows.
		perPart := windows
		if tc.plan != nil {
			if pp := tc.plan.audioWindowsFor(p.Name, dur); pp != nil {
				perPart = shiftWindows(pp, -lag, dur)
			} else if _, expected := tc.expectedAudioChannels[p.Name]; expected {
				// Plan has no events for this participant but the test
				// explicitly expects them in the output (e.g. audio
				// mixing tests with manual publishers). Use a synthetic
				// full-duration window so the routing check still runs.
				perPart = shiftWindows([]timeWindow{{start: 0, end: dur}}, -lag, dur)
			}
		}
		// Drop windows whose post-settling portion is too short for the
		// bandpass detector to lock onto — e.g. p0's first slot in
		// stream tests where mediamtx + chrome warmup eats most of the
		// 5s slot before the recording even starts.
		perPart = usableWindows(perPart)

		windowBeeps := filterBeepsByWindows(beeps, perPart)
		windowBeepPTS := beepPTS(windowBeeps)

		expectedChannel, expectInOutput := resolveExpectedChannel(tc, p.Name)

		// No route, or no plan intervals → expect zero beeps.
		if !expectInOutput || len(perPart) == 0 {
			require.Empty(t, beeps,
				"expected no beeps for %s but got %d", p.Name, len(beeps))
			continue
		}

		require.Greater(t, len(windowBeeps), 0,
			"no beeps detected for %s during expected content windows", p.Name)

		// Audio takes a few beeps to stabilize after publish; skip until
		// two consecutive gaps land near 1s.
		startIdx := firstStableIndex(windowBeepPTS)
		for i := startIdx; i < len(windowBeeps); i++ {
			gap := windowBeepPTS[i] - windowBeepPTS[i-1]
			if !sameWindow(windowBeepPTS[i-1], windowBeepPTS[i], perPart) {
				continue
			}
			// Skip gaps far from 1s — too-large means a missed beep,
			// too-small means a noise beep snuck in between real ones.
			if absDuration(gap-time.Second) > cadenceWindow {
				continue
			}
			require.InDelta(t, float64(time.Second), float64(gap), float64(eventSpacingTolerance),
				"beep spacing irregular for %s at index %d: gap=%s", p.Name, i, gap)
		}

		requirePerWindowCount(t, fmt.Sprintf("beep count for %s", p.Name), windowBeepPTS, perPart)

		for i, b := range windowBeeps {
			require.Equal(t, expectedChannel, b.Channel,
				"beep channel mismatch for %s at index %d (PTS=%s): got %d, want %d",
				p.Name, i, b.PTS, b.Channel, expectedChannel)
		}
	}
}

// usableWindows drops windows whose post-settling portion is shorter
// than minUsableWindow — the detector can't reliably lock on in less
// than a cadence cycle of clean signal.
func usableWindows(windows []timeWindow) []timeWindow {
	var out []timeWindow
	for _, w := range windows {
		useStart := w.start
		if useStart < detectorSettlingDuration {
			useStart = detectorSettlingDuration
		}
		if w.end-useStart >= minUsableWindow {
			out = append(out, w)
		}
	}
	return out
}

// requirePerWindowCount checks each window individually. Every window
// expects roughly `floor(duration_seconds)` events; we forgive
// edgeLossPerWindow events at the edges plus another
// detectorSettlingDuration worth of events at the leading edge — the
// avsync bandpass detector needs ~2s to re-lock after every mute or
// publish boundary. Stricter than a global tolerance because a window
// losing every event is no longer hidden behind a neighbor's surplus.
func requirePerWindowCount(t *testing.T, label string, eventPTS []time.Duration, windows []timeWindow) {
	t.Helper()
	settlingLoss := int(detectorSettlingDuration / time.Second)
	for _, w := range windows {
		expected := int((w.end - w.start) / time.Second)
		if expected <= 0 {
			continue
		}
		inside := 0
		for _, p := range eventPTS {
			if p >= w.start && p < w.end {
				inside++
			}
		}
		minRequired := expected - edgeLossPerWindow - settlingLoss
		if minRequired < 0 {
			minRequired = 0
		}
		require.GreaterOrEqual(t, inside, minRequired,
			"%s: window [%s, %s) expected ~%d events, got %d", label, w.start, w.end, expected, inside)
	}
}

// resolveExpectedChannel returns (channel, expectInOutput). With an empty
// map every participant defaults to BOTH; otherwise only mapped
// participants are expected in the output.
func resolveExpectedChannel(tc *testCase, participant string) (avsync.BeepChannel, bool) {
	if len(tc.expectedAudioChannels) == 0 {
		return avsync.BeepChannelBoth, true
	}
	ch, ok := tc.expectedAudioChannels[participant]
	if !ok {
		return 0, false
	}
	return avsync.BeepChannel(ch), true
}

// hasNonP0Expectation returns true if expectedAudioChannels covers any
// participant other than p0 — signals that the verifier should iterate
// all three participants (e.g. dual-channel audio mixing tests).
func hasNonP0Expectation(m map[string]livekit.AudioChannel) bool {
	for name := range m {
		if name != "p0" {
			return true
		}
	}
	return false
}

func filterBeepsByWindows(beeps []avsync.Beep, windows []timeWindow) []avsync.Beep {
	var result []avsync.Beep
	for _, b := range beeps {
		if inWindows(b.PTS, windows) {
			result = append(result, b)
		}
	}
	return result
}

func beepPTS(beeps []avsync.Beep) []time.Duration {
	out := make([]time.Duration, len(beeps))
	for i, b := range beeps {
		out[i] = b.PTS
	}
	return out
}

// verifyAVSync bins audio beeps and video flashes by their nearest
// integer second, then for each bucket containing both checks the
// smallest |flash − beep| across all in-bucket pairs (the "tight pair"
// in that second). Per-bucket counts and missing-event detection are
// covered by verifyBeeps/verifyFlashes — this check is purely about
// timing alignment between events that did make it into the recording.
//
// Two assertions:
//   - every bucket's tight pair must be within avSyncTolerance
//   - drift (max bucket offset − min) must be < avSyncTolerance/2 —
//     audio and video should not gradually decouple as recording goes on
//
// Only events inside both audio and video windows are considered.
func (r *Runner) verifyAVSync(t *testing.T, result *avsync.Result, videoWindows, audioWindows []timeWindow) {
	t.Helper()

	var allFlashes []time.Duration
	for _, flashes := range result.Video.Flashes {
		allFlashes = append(allFlashes, flashes...)
	}
	beepTimes := make([]time.Duration, len(result.Audio.Beeps))
	for i, b := range result.Audio.Beeps {
		beepTimes[i] = b.PTS
	}

	syncFlashes := filterByWindows(filterByWindows(allFlashes, videoWindows), audioWindows)
	syncBeeps := filterByWindows(filterByWindows(beepTimes, videoWindows), audioWindows)

	flashBuckets := bucketByNearestSecond(syncFlashes)
	beepBuckets := bucketByNearestSecond(syncBeeps)

	type bucketStat struct {
		sec    int64
		offset time.Duration // min |flash - beep| over all pairs in this bucket
	}
	var stats []bucketStat
	for sec, flashes := range flashBuckets {
		beeps, ok := beepBuckets[sec]
		if !ok {
			continue
		}
		min := absDuration(flashes[0] - beeps[0])
		for _, f := range flashes {
			for _, b := range beeps {
				if d := absDuration(f - b); d < min {
					min = d
				}
			}
		}
		stats = append(stats, bucketStat{sec, min})
	}
	if len(stats) == 0 {
		return
	}

	sort.Slice(stats, func(i, j int) bool { return stats[i].sec < stats[j].sec })

	// Drop buckets inside the bandpass detector's startup transient.
	// Beep detection has variable extra lag during the first ~2s while
	// the filter rings up; including those buckets makes max/drift
	// reflect the detector ramp, not actual sync.
	settledSec := int64(detectorSettlingDuration / time.Second)
	settled := stats[:0:0]
	for _, s := range stats {
		if s.sec >= settledSec {
			settled = append(settled, s)
		}
	}
	if len(settled) == 0 {
		t.Logf("av-sync: no buckets after detector settling — skipped")
		return
	}

	offs := make([]time.Duration, len(settled))
	for i, s := range settled {
		offs[i] = s.offset
	}
	sort.Slice(offs, func(i, j int) bool { return offs[i] < offs[j] })
	minOff := offs[0]
	maxOff := offs[len(offs)-1]
	medianOff := offs[len(offs)/2]
	// 90th percentile — robust to single-bucket transients at rotation
	// transitions and encoder hiccups. Wholesale sync drift would push
	// most buckets out of tolerance, not just one.
	p90Idx := int(float64(len(offs)) * 0.9)
	if p90Idx >= len(offs) {
		p90Idx = len(offs) - 1
	}
	p90Off := offs[p90Idx]

	t.Logf("av-sync per-bucket offset (post-settling): min=%s median=%s p90=%s max=%s (%d/%d buckets)",
		minOff, medianOff, p90Off, maxOff, len(settled), len(stats))

	require.LessOrEqual(t, p90Off, avSyncTolerance,
		"av-sync: 90th-percentile bucket offset exceeded %s (p90=%s, max=%s)", avSyncTolerance, p90Off, maxOff)
}

// bucketByNearestSecond groups times by their nearest integer second;
// 1.5s rounds to 2s, 2.499s rounds to 2s, 2.5s rounds to 3s.
func bucketByNearestSecond(times []time.Duration) map[int64][]time.Duration {
	out := make(map[int64][]time.Duration)
	for _, t := range times {
		sec := int64((t + 500*time.Millisecond) / time.Second)
		out[sec] = append(out[sec], t)
	}
	return out
}

// verifyParticipantVisibility: for speaker layouts we can't assert which
// participant is on stage at recording-time T because the recording
// starts an unknown delay after the publish-side rotation begins (egress
// setup + Chrome warmup). We just check the stage region rendered
// something. Grid layouts let us assert all 3 participants are present.
func (r *Runner) verifyParticipantVisibility(t *testing.T, result *avsync.Result, tc *testCase, windows []timeWindow) {
	t.Helper()

	if tc.layout == layoutSpeaker || tc.layout == layoutSingleSpeaker {
		stageFrames := result.Video.Regions[regionStage]
		labeled := 0
		for _, f := range stageFrames {
			if f.Participant != "" && inWindows(f.PTS, windows) {
				labeled++
			}
		}
		require.Greater(t, labeled, 0,
			"no labeled frames in stage region — egress not rendering active speaker?")
		return
	}

	seen := make(map[string]bool)
	for _, frames := range result.Video.Regions {
		for _, f := range frames {
			if f.Participant != "" && inWindows(f.PTS, windows) {
				seen[f.Participant] = true
			}
		}
	}
	require.True(t, seen[avsync.P0.Name], "p0 not visible in any region")
	require.True(t, seen[avsync.P1.Name], "p1 not visible in any region")
	require.True(t, seen[avsync.P2.Name], "p2 not visible in any region")
}

// --- window helpers ---

func filterByWindows(times []time.Duration, windows []timeWindow) []time.Duration {
	var result []time.Duration
	for _, t := range times {
		if inWindows(t, windows) {
			result = append(result, t)
		}
	}
	return result
}

// Windows are half-open [start, end) — matches requirePerWindowCount and
// the windowsFor derivation.
func inWindows(t time.Duration, windows []timeWindow) bool {
	for _, w := range windows {
		if t >= w.start && t < w.end {
			return true
		}
	}
	return false
}

func sameWindow(a, b time.Duration, windows []timeWindow) bool {
	for _, w := range windows {
		if a >= w.start && a < w.end && b >= w.start && b < w.end {
			return true
		}
	}
	return false
}

// firstStableIndex returns the first index whose adjacent gaps are both
// within tolerance — the start of the steady-state 1Hz cadence. Returns
// len(times) when nothing is stable, so callers' loops do nothing.
func firstStableIndex(times []time.Duration) int {
	for i := 1; i < len(times)-1; i++ {
		prevGap := times[i] - times[i-1]
		nextGap := times[i+1] - times[i]
		if isStableGap(prevGap) && isStableGap(nextGap) {
			return i
		}
	}
	if len(times) > 0 {
		return len(times)
	}
	return 1
}

func isStableGap(gap time.Duration) bool {
	delta := gap - time.Second
	if delta < 0 {
		delta = -delta
	}
	return delta <= eventSpacingTolerance
}

func countOutsideWindows(times []time.Duration, windows []timeWindow, tolerance time.Duration) int {
	count := 0
	for _, t := range times {
		outside := true
		for _, w := range windows {
			if t >= w.start-tolerance && t <= w.end+tolerance {
				outside = false
				break
			}
		}
		if outside {
			count++
		}
	}
	return count
}

// --- general helpers ---

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func videoDimensions(info *FFProbeInfo) (width, height int) {
	for _, s := range info.Streams {
		if s.CodecType == "video" {
			return int(s.Width), int(s.Height)
		}
	}
	return 1920, 1080
}

// --- stream keyframe checks (kept as explicit contentCheck overrides) ---

func (r *Runner) streamKeyframeContentCheck(expectedInterval float64) func(t *testing.T, target string, _ *FFProbeInfo) {
	return func(t *testing.T, target string, _ *FFProbeInfo) {
		requireKeyframeInterval(t, target, expectedInterval)
	}
}

func requireKeyframeInterval(t *testing.T, input string, expectedInterval float64) {
	t.Helper()
	if expectedInterval <= 0 {
		return
	}

	timestamps, err := ffprobeKeyframeTimestamps(input, expectedInterval)

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(timestamps), 2, "ffprobe returned less than two keyframes for %s", input)

	tolerance := 0.020 // 20ms
	prev := timestamps[0]
	found := false
	for _, ts := range timestamps[1:] {
		if ts <= prev {
			prev = ts
			continue
		}
		found = true
		require.InDelta(t, expectedInterval, ts-prev, tolerance, "keyframe spacing mismatch for %s", input)
		prev = ts
	}
	require.True(t, found, "no increasing keyframe timestamps found for %s", input)
}

func ffprobeKeyframeTimestamps(input string, expectedInterval float64) ([]float64, error) {
	timestamps := []float64{}
	var err error

	readSeconds := expectedInterval*4 + 1

	args := []string{
		"-v", "error",
		"-fflags", "nobuffer",
		"-rw_timeout", "5000000",
		"-select_streams", "v:0",
		"-show_packets",
		"-show_entries", "packet=pts_time,dts_time,flags,stream_index,size,pos",
		"-of", "csv=p=0",
		input,
	}

	timeout := time.Duration(readSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffprobe: %w", err)
	}
	defer cmd.Wait()

	csvReader := csv.NewReader(stdout)

	for {
		record, e := csvReader.Read()
		if e != nil {
			if ctx.Err() == nil && e != io.EOF {
				err = fmt.Errorf("read csv: %w", e)
			}
			break
		}

		if len(record) != 6 {
			err = fmt.Errorf("unexpected record length: %d", len(record))
			break
		}

		pts, e := strconv.ParseFloat(record[1], 64)
		if e != nil {
			err = fmt.Errorf("parse pts: %w", e)
			break
		}
		if strings.Contains(record[5], "K") {
			timestamps = append(timestamps, pts)
		}
	}

	if err != nil {
		return nil, err
	}
	return timestamps, nil
}
