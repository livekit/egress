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

	"github.com/livekit/egress/pkg/types"

	"github.com/livekit/media-samples/avsync"
	"github.com/livekit/protocol/logger"
)

const (
	eventSpacingTolerance = 20 * time.Millisecond
	avSyncTolerance       = 100 * time.Millisecond
	eventCountTolerance   = 5

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

// runContentCheck derives the appropriate content check from the TestConfig.
func (r *Runner) runContentCheck(t *testing.T, cfg *TestConfig, file string, info *FFProbeInfo) {
	if info == nil {
		return
	}

	// Skip content checks for Web request type (external URL, not our test samples)
	if cfg.RequestType == types.RequestTypeWeb {
		return
	}

	// Determine participants and regions.
	// Media egress records individual tracks (not composited), so video is
	// always a single participant even when MultiParticipant is set for audio routing.
	multiParticipantVideo := cfg.MultiParticipant && cfg.RequestType != types.RequestTypeMedia
	participants := []avsync.Participant{avsync.P0}
	if multiParticipantVideo {
		participants = avsync.AllParticipants
	}

	w, h := videoDimensions(info)
	var regions []avsync.Region

	switch {
	case multiParticipantVideo && cfg.Layout == layoutSpeaker:
		regions = SpeakerLayoutRegions(w, h, len(participants))
	case multiParticipantVideo && cfg.Layout == layoutSingleSpeaker:
		regions = SingleSpeakerLayoutRegions(w, h)
	case multiParticipantVideo && cfg.Layout == layoutGrid:
		regions = GridLayoutRegions(w, h, len(participants))
	case multiParticipantVideo:
		regions = GridLayoutRegions(w, h, len(participants))
	case !cfg.AudioOnly:
		regions = []avsync.Region{{Name: regionFull, Rect: image.Rect(0, 0, w, h)}}
	}

	result, err := avsync.Analyze(avsync.Config{
		FilePath:     file,
		Regions:      regions,
		Participants: participants,
	})
	require.NoError(t, err)

	dur, _ := parseFFProbeDuration(info.Format.Duration)

	// Log all detected events for debugging
	for regionName, flashes := range result.Video.Flashes {
		t.Logf("content check: %d flashes in region %s:", len(flashes), regionName)
		for i, f := range flashes {
			if i > 0 {
				t.Logf("  flash[%d] = %s (gap = %s)", i, f, f-flashes[i-1])
			} else {
				t.Logf("  flash[%d] = %s", i, f)
			}
		}
	}
	for _, p := range participants {
		var beeps []time.Duration
		for _, b := range result.Audio.Beeps {
			if b.Participant == p.Name {
				beeps = append(beeps, b.PTS)
			}
		}
		t.Logf("content check: %d beeps for %s:", len(beeps), p.Name)
		for i, b := range beeps {
			if i > 0 {
				t.Logf("  beep[%d] = %s (gap = %s)", i, b, b-beeps[i-1])
			} else {
				t.Logf("  beep[%d] = %s", i, b)
			}
		}
	}
	t.Logf("content check: file duration = %s", dur)

	var videoWindows []timeWindow
	if !cfg.AudioOnly {
		videoWindows = []timeWindow{{start: 0, end: dur}}
	}
	var audioWindows []timeWindow
	if !cfg.VideoOnly {
		audioWindows = []timeWindow{{start: 0, end: dur}}
	}

	// --- Video checks ---
	if !cfg.AudioOnly && len(videoWindows) > 0 {
		r.verifyFlashes(t, result, videoWindows)

		if multiParticipantVideo {
			r.verifyParticipantVisibility(t, result, cfg, videoWindows)
		}
	}

	// --- Audio checks ---
	if !cfg.VideoOnly && len(audioWindows) > 0 {
		r.verifyBeeps(t, result, audioWindows, participants)
	}

	// --- A/V sync check ---
	if !cfg.AudioOnly && !cfg.VideoOnly && len(videoWindows) > 0 && len(audioWindows) > 0 {
		r.verifyAVSync(t, result, videoWindows)
	}
}

// verifyFlashes checks flash consistency within expected content windows.
func (r *Runner) verifyFlashes(t *testing.T, result *avsync.Result, windows []timeWindow) {
	t.Helper()
	for regionName, flashes := range result.Video.Flashes {
		windowFlashes := filterByWindows(flashes, windows)
		require.Greater(t, len(windowFlashes), 0,
			"no flashes in region %s during expected content windows", regionName)

		// Check spacing — allow up to eventCountTolerance irregular gaps
		// (network jitter can cause occasional missed events)
		irregularGaps := 0
		for i := 1; i < len(windowFlashes); i++ {
			gap := windowFlashes[i] - windowFlashes[i-1]
			if sameWindow(windowFlashes[i-1], windowFlashes[i], windows) {
				if absDuration(gap-time.Second) > eventSpacingTolerance {
					irregularGaps++
				}
			}
		}
		require.Equal(t, 0, irregularGaps,
			"irregular flash gaps in region %s", regionName)

		// Check count against actual detected span (first to last flash)
		if len(windowFlashes) >= 2 {
			span := windowFlashes[len(windowFlashes)-1] - windowFlashes[0]
			expectedFromSpan := span.Seconds() + 1
			require.InDelta(t, expectedFromSpan, float64(len(windowFlashes)), 1,
				"flash count mismatch in region %s (span=%s)", regionName, span)
		}

		logger.Debugw("verifyFlashes", "region", regionName,
			"total", len(flashes), "inWindow", len(windowFlashes), "irregularGaps", irregularGaps)
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
		require.Greater(t, len(windowBeeps), 0,
			"no beeps detected for %s during expected content windows", p.Name)

		// Check spacing — allow up to eventCountTolerance irregular gaps
		irregularGaps := 0
		for i := 1; i < len(windowBeeps); i++ {
			gap := windowBeeps[i] - windowBeeps[i-1]
			if sameWindow(windowBeeps[i-1], windowBeeps[i], windows) {
				if absDuration(gap-time.Second) > eventSpacingTolerance {
					irregularGaps++
				}
			}
		}
		require.LessOrEqual(t, irregularGaps, eventCountTolerance,
			"too many irregular beep gaps (%d) for %s", irregularGaps, p.Name)

		// Check count against actual detected span (first to last beep),
		// not file duration — beeps may start late and end early.
		if len(windowBeeps) >= 2 {
			span := windowBeeps[len(windowBeeps)-1] - windowBeeps[0]
			expectedFromSpan := span.Seconds() + 1 // +1 for the first beep
			require.InDelta(t, expectedFromSpan, float64(len(windowBeeps)), eventCountTolerance,
				"beep count mismatch for %s (span=%s)", p.Name, span)
		}

		logger.Debugw("verifyBeeps", "participant", p.Name,
			"total", len(beeps), "inWindow", len(windowBeeps), "irregularGaps", irregularGaps)
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

	require.LessOrEqual(t, unmatchedFlashes, eventCountTolerance,
		"%d flashes had no matching beep within %s", unmatchedFlashes, avSyncTolerance)
	require.LessOrEqual(t, unmatchedBeeps, eventCountTolerance,
		"%d beeps had no matching flash within %s", unmatchedBeeps, avSyncTolerance)

	logger.Debugw("verifyAVSync",
		"flashes", len(allFlashes), "beeps", len(windowBeeps),
		"unmatchedFlashes", unmatchedFlashes, "unmatchedBeeps", unmatchedBeeps)
}

// verifyParticipantVisibility checks that expected participants are visible
// in their respective regions during content windows.
func (r *Runner) verifyParticipantVisibility(t *testing.T, result *avsync.Result, cfg *TestConfig, windows []timeWindow) {
	t.Helper()

	if cfg.Layout == layoutSpeaker || cfg.Layout == layoutSingleSpeaker {
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

// --- stream keyframe checks ---

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
