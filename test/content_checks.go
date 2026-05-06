// Copyright 2023 LiveKit, Inc.
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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-samples/avsync"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/pkg/types"
)

const (
	eventSpacingTolerance = 20 * time.Millisecond
	avSyncTolerance       = 100 * time.Millisecond
	eventCountTolerance   = 3

	layoutSpeaker       = "speaker"
	layoutSingleSpeaker = "single-speaker"
	layoutGrid          = "grid"

	regionStage = "stage"
	regionFull  = "full"

	// Muting schedule constants (must match publishSample in publish.go)
	muteStartDelay   = 15 * time.Second
	muteTogglePeriod = 10 * time.Second
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

	// Explicit override (used for stream keyframe checks)
	if tc.contentCheck != nil {
		tc.contentCheck(t, file, info)
		return
	}

	// Web and WebV2 tests load arbitrary content from a URL, not the avsync
	// test pattern, so beep/flash detection doesn't apply.
	if tc.requestType == types.RequestTypeWeb {
		return
	}

	// Determine participants and regions
	participants := []avsync.Participant{avsync.P0}
	if tc.multiParticipant {
		participants = avsync.AllParticipants
	}

	w, h := videoDimensions(info)
	var regions []avsync.Region

	switch {
	case tc.multiParticipant && tc.layout == layoutSpeaker:
		regions = SpeakerLayoutRegions(w, h, len(participants))
	case tc.multiParticipant && tc.layout == layoutSingleSpeaker:
		regions = SingleSpeakerLayoutRegions(w, h)
	case tc.multiParticipant && tc.layout == layoutGrid:
		regions = GridLayoutRegions(w, h, len(participants))
	case tc.multiParticipant:
		regions = GridLayoutRegions(w, h, len(participants))
	case !tc.audioOnly:
		regions = []avsync.Region{{Name: regionFull, Rect: image.Rect(0, 0, w, h)}}
	}

	result, err := avsync.Analyze(avsync.Config{
		FilePath:     file,
		Regions:      regions,
		Participants: participants,
	})
	require.NoError(t, err)

	logger.Infow("avsync results",
		"audio", result.Audio.Beeps,
		"silence", result.Audio.Silence,
		"video", result.Video.Flashes,
		"regions", result.Video.Regions,
	)

	dur, _ := parseFFProbeDuration(info.Format.Duration)

	// Compute expected content windows
	videoWindows := computeVideoWindows(tc, dur, r.Muting)
	audioWindows := computeAudioWindows(tc, dur, r.Muting)

	// --- Video checks ---
	if !tc.audioOnly && len(videoWindows) > 0 {
		r.verifyFlashes(t, result, videoWindows)

		if tc.multiParticipant {
			r.verifyParticipantVisibility(t, result, tc, videoWindows)
		}
	}

	// --- Audio checks ---
	if !tc.videoOnly && len(audioWindows) > 0 {
		r.verifyBeeps(t, result, audioWindows, participants)
	}

	// --- A/V sync check ---
	if !tc.audioOnly && !tc.videoOnly && len(videoWindows) > 0 && len(audioWindows) > 0 {
		r.verifyAVSync(t, result, videoWindows)
	}
}

// computeVideoWindows returns time windows where video content (flashes) is expected.
func computeVideoWindows(tc *testCase, dur time.Duration, muting bool) []timeWindow {
	if tc.audioOnly {
		return nil
	}

	start := tc.videoDelay
	end := dur

	// Build initial windows from publish/unpublish/republish
	var windows []timeWindow
	if tc.videoUnpublish > 0 {
		windows = append(windows, timeWindow{start, tc.videoUnpublish})
		if tc.videoRepublish > 0 {
			windows = append(windows, timeWindow{tc.videoRepublish, end})
		}
	} else {
		windows = append(windows, timeWindow{start, end})
	}

	// Apply muting gaps
	if muting {
		windows = applyMuting(windows, tc.videoDelay)
	}

	return windows
}

// computeAudioWindows returns time windows where audio content (beeps) is expected.
func computeAudioWindows(tc *testCase, dur time.Duration, muting bool) []timeWindow {
	if tc.videoOnly {
		return nil
	}

	start := tc.audioDelay
	end := dur

	var windows []timeWindow
	if tc.audioUnpublish > 0 {
		windows = append(windows, timeWindow{start, tc.audioUnpublish})
		if tc.audioRepublish > 0 {
			windows = append(windows, timeWindow{tc.audioRepublish, end})
		}
	} else {
		windows = append(windows, timeWindow{start, end})
	}

	if muting {
		windows = applyMuting(windows, tc.audioDelay)
	}

	return windows
}

// applyMuting splits windows based on the muting schedule.
// Muting starts at publishDelay + muteStartDelay, toggles every muteTogglePeriod.
// Unmuted windows are kept, muted windows are removed.
func applyMuting(windows []timeWindow, publishDelay time.Duration) []timeWindow {
	muteStart := publishDelay + muteStartDelay

	var result []timeWindow
	for _, w := range windows {
		result = append(result, splitByMuting(w, muteStart)...)
	}
	return result
}

// splitByMuting takes a single window and returns the unmuted sub-windows.
func splitByMuting(w timeWindow, muteStart time.Duration) []timeWindow {
	// Before muting starts, everything is unmuted
	if w.end <= muteStart {
		return []timeWindow{w}
	}

	var result []timeWindow

	// Portion before muting starts
	if w.start < muteStart {
		result = append(result, timeWindow{w.start, muteStart})
	}

	// Walk through mute/unmute cycles
	// At muteStart: muted for muteTogglePeriod
	// At muteStart + muteTogglePeriod: unmuted for muteTogglePeriod
	// etc.
	cursor := muteStart
	muted := true
	for cursor < w.end {
		cycleEnd := cursor + muteTogglePeriod
		if cycleEnd > w.end {
			cycleEnd = w.end
		}

		if !muted {
			segStart := cursor
			if segStart < w.start {
				segStart = w.start
			}
			if segStart < cycleEnd {
				result = append(result, timeWindow{segStart, cycleEnd})
			}
		}

		cursor = cursor + muteTogglePeriod
		muted = !muted
	}

	return result
}

// verifyFlashes checks flash consistency within expected content windows.
func (r *Runner) verifyFlashes(t *testing.T, result *avsync.Result, windows []timeWindow) {
	t.Helper()
	for regionName, flashes := range result.Video.Flashes {
		windowFlashes := filterByWindows(flashes, windows)

		logger.Infow("verifyFlashes", "region", regionName,
			"total", len(flashes), "inWindow", len(windowFlashes),
			"flashes", flashes, "windowFlashes", windowFlashes,
			"windows", formatWindows(windows))

		require.Greater(t, len(windowFlashes), 0,
			"no flashes in region %s during expected content windows", regionName)

		// Check spacing within windows
		for i := 1; i < len(windowFlashes); i++ {
			gap := windowFlashes[i] - windowFlashes[i-1]
			// Only check spacing for consecutive flashes within the same window
			if sameWindow(windowFlashes[i-1], windowFlashes[i], windows) {
				require.InDelta(t, float64(time.Second), float64(gap), float64(eventSpacingTolerance),
					"flash spacing irregular at index %d in region %s: gap=%s", i, regionName, gap)
			}
		}

		// Check total count against expected duration of content windows
		expectedSeconds := totalWindowDuration(windows).Seconds()
		require.InDelta(t, expectedSeconds, float64(len(windowFlashes)), eventCountTolerance,
			"flash count mismatch in region %s", regionName)

		// Check no flashes outside windows (allow tolerance at boundaries)
		outsideCount := countOutsideWindows(flashes, windows, 500*time.Millisecond)
		require.LessOrEqual(t, outsideCount, 2,
			"unexpected flashes outside content windows in region %s", regionName)
	}
}

// verifyBeeps checks beep consistency within expected content windows.
func (r *Runner) verifyBeeps(t *testing.T, result *avsync.Result, windows []timeWindow, participants []avsync.Participant) {
	t.Helper()

	beepsByParticipant := make(map[string][]time.Duration)
	for _, b := range result.Audio.Beeps {
		beepsByParticipant[b.Participant] = append(beepsByParticipant[b.Participant], b.PTS)
	}

	for _, p := range participants {
		beeps := beepsByParticipant[p.Name]
		windowBeeps := filterByWindows(beeps, windows)

		logger.Infow("verifyBeeps", "participant", p.Name,
			"total", len(beeps), "inWindow", len(windowBeeps),
			"beeps", beeps, "windowBeeps", windowBeeps,
			"windows", formatWindows(windows))

		require.Greater(t, len(windowBeeps), 0,
			"no beeps detected for %s during expected content windows", p.Name)

		// Check spacing within windows
		for i := 1; i < len(windowBeeps); i++ {
			gap := windowBeeps[i] - windowBeeps[i-1]
			if sameWindow(windowBeeps[i-1], windowBeeps[i], windows) {
				require.InDelta(t, float64(time.Second), float64(gap), float64(eventSpacingTolerance),
					"beep spacing irregular for %s at index %d: gap=%s", p.Name, i, gap)
			}
		}

		expectedSeconds := totalWindowDuration(windows).Seconds()
		require.InDelta(t, expectedSeconds, float64(len(windowBeeps)), eventCountTolerance,
			"beep count mismatch for %s", p.Name)
	}
}

// verifyAVSync checks that flashes and beeps are synchronized within content windows.
func (r *Runner) verifyAVSync(t *testing.T, result *avsync.Result, videoWindows []timeWindow) {
	t.Helper()

	var allFlashes []time.Duration
	for _, flashes := range result.Video.Flashes {
		allFlashes = append(allFlashes, filterByWindows(flashes, videoWindows)...)
	}
	if len(allFlashes) == 0 || len(result.Audio.Beeps) == 0 {
		return
	}

	beepTimes := make([]time.Duration, len(result.Audio.Beeps))
	for i, b := range result.Audio.Beeps {
		beepTimes[i] = b.PTS
	}

	// For each flash in a content window, find the nearest beep
	unmatchedFlashes := 0
	for _, flash := range allFlashes {
		nearest := findNearest(beepTimes, flash)
		delta := absDuration(flash - nearest)
		if delta > avSyncTolerance {
			unmatchedFlashes++
		}
	}

	// For each beep in a content window, find the nearest flash
	windowBeeps := filterByWindows(beepTimes, videoWindows)
	unmatchedBeeps := 0
	for _, beep := range windowBeeps {
		nearest := findNearest(allFlashes, beep)
		delta := absDuration(beep - nearest)
		if delta > avSyncTolerance {
			unmatchedBeeps++
		}
	}

	maxUnmatched := 3
	require.LessOrEqual(t, unmatchedFlashes, maxUnmatched,
		"%d flashes had no matching beep within %s", unmatchedFlashes, avSyncTolerance)
	require.LessOrEqual(t, unmatchedBeeps, maxUnmatched,
		"%d beeps had no matching flash within %s", unmatchedBeeps, avSyncTolerance)

	logger.Debugw("verifyAVSync",
		"flashes", len(allFlashes), "beeps", len(windowBeeps),
		"unmatchedFlashes", unmatchedFlashes, "unmatchedBeeps", unmatchedBeeps)
}

// verifyParticipantVisibility checks that expected participants are visible
// in their respective regions during content windows.
func (r *Runner) verifyParticipantVisibility(t *testing.T, result *avsync.Result, tc *testCase, windows []timeWindow) {
	t.Helper()

	if tc.layout == layoutSpeaker || tc.layout == layoutSingleSpeaker {
		stageFrames := result.Video.Regions[regionStage]
		if len(stageFrames) == 0 {
			return
		}

		// 5s rotation: p0 @ 0-5s, p1 @ 5-10s, p2 @ 10-15s, ...
		// Sample 2s into each interval to allow transition time.
		// Only check times that fall within content windows.
		expected := []struct {
			time        time.Duration
			participant string
		}{
			{2 * time.Second, avsync.P0.Name},
			{7 * time.Second, avsync.P1.Name},
			{12 * time.Second, avsync.P2.Name},
			{17 * time.Second, avsync.P0.Name},
			{22 * time.Second, avsync.P1.Name},
		}

		for _, exp := range expected {
			if !inWindows(exp.time, windows) {
				continue
			}
			frame := findFrameAt(stageFrames, exp.time)
			if frame != nil {
				require.Equal(t, exp.participant, frame.Participant,
					"wrong participant on stage at %s", exp.time)
			}
		}
	} else {
		// Grid or default layout: verify all participants appear somewhere
		// during content windows.
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
}

// --- window helpers ---

// filterByWindows returns only timestamps that fall within any of the given windows.
func filterByWindows(times []time.Duration, windows []timeWindow) []time.Duration {
	var result []time.Duration
	for _, t := range times {
		if inWindows(t, windows) {
			result = append(result, t)
		}
	}
	return result
}

// inWindows returns true if t falls within any window.
func inWindows(t time.Duration, windows []timeWindow) bool {
	for _, w := range windows {
		if t >= w.start && t <= w.end {
			return true
		}
	}
	return false
}

// sameWindow returns true if both timestamps fall within the same window.
func sameWindow(a, b time.Duration, windows []timeWindow) bool {
	for _, w := range windows {
		if a >= w.start && a <= w.end && b >= w.start && b <= w.end {
			return true
		}
	}
	return false
}

// formatWindows returns a slice of "[start..end]" strings for log output.
func formatWindows(windows []timeWindow) []string {
	out := make([]string, len(windows))
	for i, w := range windows {
		out[i] = fmt.Sprintf("[%s..%s]", w.start, w.end)
	}
	return out
}

// totalWindowDuration returns the sum of all window durations.
func totalWindowDuration(windows []timeWindow) time.Duration {
	var total time.Duration
	for _, w := range windows {
		total += w.end - w.start
	}
	return total
}

// countOutsideWindows counts timestamps that are outside all windows by more than tolerance.
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

func findNearest(times []time.Duration, target time.Duration) time.Duration {
	best := times[0]
	bestDelta := absDuration(target - best)
	for _, t := range times[1:] {
		delta := absDuration(target - t)
		if delta < bestDelta {
			bestDelta = delta
			best = t
		}
	}
	return best
}

func findFrameAt(frames []avsync.RegionFrame, target time.Duration) *avsync.RegionFrame {
	var best *avsync.RegionFrame
	bestDelta := time.Duration(1<<63 - 1)
	for i := range frames {
		delta := absDuration(frames[i].PTS - target)
		if delta < bestDelta {
			bestDelta = delta
			best = &frames[i]
		}
	}
	if best != nil && bestDelta > 500*time.Millisecond {
		return nil
	}
	return best
}

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
