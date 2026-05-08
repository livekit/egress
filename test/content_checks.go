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
	"encoding/json"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linkdata/deadlock"
	"github.com/stretchr/testify/require"

	"github.com/livekit/media-samples/avsync"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/pkg/types"
)

const (
	// 1Hz cadence jitter budget: 3-frame slip at 25fps is 120ms, observed
	// up to 114ms at mute-rotation slot boundaries.
	eventSpacingTolerance = 120 * time.Millisecond
	// Encoder pipeline PTS offset between audio and video runs ~200–280ms.
	avSyncTolerance = 300 * time.Millisecond
	// Gap size that means "one event missing"; spacing checks skip these.
	missedEventGap = 1500 * time.Millisecond
	// Loose tolerance: rotation can cost a participant ~3 trailing beeps,
	// and stage-region flash detection drops ~1 per active-speaker swap.
	// Stats log records exact counts so regressions are still visible.
	eventCountTolerance = 7

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
	if tc.multiParticipant {
		participants = avsync.AllParticipants
	}

	w, h := videoDimensions(info)
	var regions []avsync.Region

	// Audio-only files have no video stream; ffmpeg crop filters fail.
	switch {
	case tc.audioOnly:
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
	if tc.plan != nil {
		videoWindows = tc.plan.videoWindows(dur)
		audioWindows = tc.plan.audioWindows(dur)
		if requiresWebRenderingGrace(tc) {
			videoWindows = applyWebRenderingGrace(videoWindows, webRenderingGrace)
			audioWindows = applyWebRenderingGrace(audioWindows, webRenderingGrace)
		}
	}

	logContentStats(tc, result, videoWindows, audioWindows)

	if !tc.audioOnly && len(videoWindows) > 0 {
		r.verifyFlashes(t, result, videoWindows)

		if tc.multiParticipant {
			r.verifyParticipantVisibility(t, result, tc, videoWindows)
		}
	}

	if !tc.videoOnly && len(audioWindows) > 0 {
		r.verifyBeeps(t, tc, result, audioWindows, participants)
	}

	// AV-sync check is unreliable across publish/unpublish/disconnect/mute
	// transitions; the stats log still records the offsets.
	hasGaps := planHasGaps(tc.plan)
	if !tc.audioOnly && !tc.videoOnly && len(videoWindows) > 0 && len(audioWindows) > 0 && !hasGaps {
		r.verifyAVSync(t, result, videoWindows, audioWindows)
	}
}

func planHasGaps(p *publishPlan) bool {
	if p == nil {
		return false
	}
	for _, s := range p.audio {
		if len(s.gaps) > 0 {
			return true
		}
	}
	for _, s := range p.video {
		if len(s.gaps) > 0 {
			return true
		}
	}
	return false
}

// webRenderingGrace is the time it takes a Chrome-rendered template
// (RoomComposite / Template) to load and start producing content. The plan
// represents SDK publish times — for web-rendered tests the recording lags
// the publish by this amount, so we clip the verifier's leading window
// edge forward by this much.
const webRenderingGrace = 3 * time.Second

// requiresWebRenderingGrace returns true for request types whose recording
// begins with ~3 seconds of empty frames while the template loads in Chrome.
func requiresWebRenderingGrace(tc *testCase) bool {
	return tc.requestType == types.RequestTypeRoomComposite ||
		tc.requestType == types.RequestTypeTemplate
}

// applyWebRenderingGrace pushes each window's leading edge forward by
// grace, dropping windows that fall entirely before it.
func applyWebRenderingGrace(windows []timeWindow, grace time.Duration) []timeWindow {
	var out []timeWindow
	for _, w := range windows {
		if w.end <= grace {
			continue
		}
		if w.start < grace {
			w.start = grace
		}
		out = append(out, w)
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
			// A whole-event-wide gap is a missing flash, not jitter.
			if gap >= missedEventGap {
				continue
			}
			require.InDelta(t, float64(time.Second), float64(gap), float64(eventSpacingTolerance),
				"flash spacing irregular at index %d in region %s: gap=%s", i, regionName, gap)
		}

		expectedSeconds := totalWindowDuration(windows).Seconds()
		require.InDelta(t, expectedSeconds, float64(len(windowFlashes)), eventCountTolerance,
			"flash count mismatch in region %s", regionName)

		// Web-rendered tests emit extra leading flashes during Chrome
		// warmup; tolerate the full warmup period at the boundary.
		boundaryTolerance := 500 * time.Millisecond
		if len(windows) > 0 && windows[0].start > boundaryTolerance {
			boundaryTolerance = windows[0].start
		}
		outsideCount := countOutsideWindows(flashes, windows, boundaryTolerance)
		require.LessOrEqual(t, outsideCount, 2,
			"unexpected flashes outside content windows in region %s", regionName)
	}
}

func (r *Runner) verifyBeeps(t *testing.T, tc *testCase, result *avsync.Result, windows []timeWindow, participants []avsync.Participant) {
	t.Helper()

	beepsByParticipant := make(map[string][]avsync.Beep)
	for _, b := range result.Audio.Beeps {
		beepsByParticipant[b.Participant] = append(beepsByParticipant[b.Participant], b)
	}

	for _, p := range participants {
		beeps := beepsByParticipant[p.Name]

		// Per-participant windows for filter, spacing, and count: a leaked
		// beep from a muted participant otherwise escapes the union filter.
		perPart := windows
		if tc.plan != nil {
			if pp := tc.plan.audioWindowsFor(p.Name, lastWindowEnd(windows)); pp != nil {
				perPart = pp
			}
		}

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

		// Audio takes 5–7 beeps to stabilize after publish; skip until
		// two consecutive gaps land near 1s.
		startIdx := firstStableIndex(windowBeepPTS)
		for i := startIdx; i < len(windowBeeps); i++ {
			gap := windowBeepPTS[i] - windowBeepPTS[i-1]
			if !sameWindow(windowBeepPTS[i-1], windowBeepPTS[i], perPart) {
				continue
			}
			if gap >= missedEventGap {
				continue
			}
			require.InDelta(t, float64(time.Second), float64(gap), float64(eventSpacingTolerance),
				"beep spacing irregular for %s at index %d: gap=%s", p.Name, i, gap)
		}

		expectedSeconds := totalWindowDuration(perPart).Seconds()
		require.InDelta(t, expectedSeconds, float64(len(windowBeeps)), eventCountTolerance,
			"beep count mismatch for %s", p.Name)

		for i, b := range windowBeeps {
			require.Equal(t, expectedChannel, b.Channel,
				"beep channel mismatch for %s at index %d (PTS=%s): got %d, want %d",
				p.Name, i, b.PTS, b.Channel, expectedChannel)
		}
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

// verifyAVSync only checks events inside both audio and video windows —
// flashes during audio-only gaps must not contribute to unmatched counts.
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

	if len(syncFlashes) == 0 || len(syncBeeps) == 0 {
		return
	}

	// findNearest must search the joint-window pool; otherwise an
	// out-of-window peer could spuriously match a window-edge event.
	unmatchedFlashes := 0
	for _, flash := range syncFlashes {
		if absDuration(flash-findNearest(syncBeeps, flash)) > avSyncTolerance {
			unmatchedFlashes++
		}
	}
	unmatchedBeeps := 0
	for _, beep := range syncBeeps {
		if absDuration(beep-findNearest(syncFlashes, beep)) > avSyncTolerance {
			unmatchedBeeps++
		}
	}

	maxUnmatched := 3
	require.LessOrEqual(t, unmatchedFlashes, maxUnmatched,
		"%d flashes had no matching beep within %s", unmatchedFlashes, avSyncTolerance)
	require.LessOrEqual(t, unmatchedBeeps, maxUnmatched,
		"%d beeps had no matching flash within %s", unmatchedBeeps, avSyncTolerance)
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

func inWindows(t time.Duration, windows []timeWindow) bool {
	for _, w := range windows {
		if t >= w.start && t <= w.end {
			return true
		}
	}
	return false
}

func sameWindow(a, b time.Duration, windows []timeWindow) bool {
	for _, w := range windows {
		if a >= w.start && a <= w.end && b >= w.start && b <= w.end {
			return true
		}
	}
	return false
}

func totalWindowDuration(windows []timeWindow) time.Duration {
	var total time.Duration
	for _, w := range windows {
		total += w.end - w.start
	}
	return total
}

func lastWindowEnd(windows []timeWindow) time.Duration {
	if len(windows) == 0 {
		return 0
	}
	return windows[len(windows)-1].end
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

// --- content stats ---

// contentStats is the per-test diagnostic payload. Stable* fields exclude
// the startup transient (gated by firstStableIndex). AV-sync offsets are
// signed: positive = video lags audio.
type contentStats struct {
	integrationType string
	test            string
	requestType     string
	audioCodec      string
	videoCodec      string
	layout          string
	audioOnly       bool
	videoOnly       bool

	flashes         int
	beeps           int
	expectedFlashes int
	expectedBeeps   int

	firstFlash time.Duration
	firstBeep  time.Duration

	timeToStableVideo time.Duration
	timeToStableAudio time.Duration

	spacingStdDevVideo time.Duration
	spacingStdDevAudio time.Duration
	maxSpacingDevVideo time.Duration
	maxSpacingDevAudio time.Duration

	stableAVSync time.Duration
	avSyncStdDev time.Duration
	maxAVSync    time.Duration

	missedFlashes    int
	missedBeeps      int
	unmatchedFlashes int
	unmatchedBeeps   int

	score float64
}

// allStats is dumped to JSON by DumpContentStats at end of TestEgress.
var (
	allStats   []contentStats
	allStatsMu deadlock.Mutex
)

func logContentStats(tc *testCase, result *avsync.Result, videoWindows, audioWindows []timeWindow) {
	s := computeContentStats(result, videoWindows, audioWindows)
	s.score = math.Round(scoreContent(s)*10) / 10

	s.integrationType = os.Getenv("INTEGRATION_TYPE")
	s.test = tc.name
	s.requestType = string(tc.requestType)
	s.audioCodec = string(tc.audioCodec)
	s.videoCodec = string(tc.videoCodec)
	s.layout = tc.layout
	s.audioOnly = tc.audioOnly
	s.videoOnly = tc.videoOnly

	logger.Infow("avsync stats",
		"test", s.test,
		"requestType", s.requestType,
		"audioCodec", s.audioCodec,
		"videoCodec", s.videoCodec,
		"layout", s.layout,
		"audioOnly", s.audioOnly,
		"videoOnly", s.videoOnly,
		"score", s.score,
		"flashes", s.flashes,
		"beeps", s.beeps,
		"expectedFlashes", s.expectedFlashes,
		"expectedBeeps", s.expectedBeeps,
		"firstFlash", s.firstFlash,
		"firstBeep", s.firstBeep,
		"timeToStableVideo", s.timeToStableVideo,
		"timeToStableAudio", s.timeToStableAudio,
		"spacingStdDevVideo", s.spacingStdDevVideo,
		"spacingStdDevAudio", s.spacingStdDevAudio,
		"maxSpacingDevVideo", s.maxSpacingDevVideo,
		"maxSpacingDevAudio", s.maxSpacingDevAudio,
		"stableAVSync", s.stableAVSync,
		"avSyncStdDev", s.avSyncStdDev,
		"maxAVSync", s.maxAVSync,
		"missedFlashes", s.missedFlashes,
		"missedBeeps", s.missedBeeps,
		"unmatchedFlashes", s.unmatchedFlashes,
		"unmatchedBeeps", s.unmatchedBeeps,
	)

	allStatsMu.Lock()
	allStats = append(allStats, s)
	allStatsMu.Unlock()
}

// DumpContentStats writes the run's stats to AVSYNC_STATS_PATH (default
// /tmp/avsync-stats.json). The entrypoint script wraps the file in sentinel
// markers AFTER go test exits; writing from inside the test process would
// race with zap's log flush during testing.T cleanup.
func DumpContentStats() {
	allStatsMu.Lock()
	defer allStatsMu.Unlock()

	out := make([]map[string]any, 0, len(allStats))
	for _, s := range allStats {
		// Inapplicable stats become JSON null so the rendered table shows
		// "N/A" instead of "0" (which is also a valid value).
		var (
			flashes            any = s.flashes
			firstFlash         any = s.firstFlash.String()
			timeToStableVideo  any = s.timeToStableVideo.String()
			spacingStdDevVideo any = s.spacingStdDevVideo.String()
			maxSpacingDevVideo any = s.maxSpacingDevVideo.String()
			missedFlashes      any = s.missedFlashes
			beeps              any = s.beeps
			firstBeep          any = s.firstBeep.String()
			timeToStableAudio  any = s.timeToStableAudio.String()
			spacingStdDevAudio any = s.spacingStdDevAudio.String()
			maxSpacingDevAudio any = s.maxSpacingDevAudio.String()
			missedBeeps        any = s.missedBeeps
			stableAVSync       any = s.stableAVSync.String()
			avSyncStdDev       any = s.avSyncStdDev.String()
			maxAVSync          any = s.maxAVSync.String()
			unmatchedFlashes   any = s.unmatchedFlashes
			unmatchedBeeps     any = s.unmatchedBeeps
		)
		if s.audioOnly {
			flashes, firstFlash, timeToStableVideo = nil, nil, nil
			spacingStdDevVideo, maxSpacingDevVideo, missedFlashes = nil, nil, nil
		}
		if s.videoOnly {
			beeps, firstBeep, timeToStableAudio = nil, nil, nil
			spacingStdDevAudio, maxSpacingDevAudio, missedBeeps = nil, nil, nil
		}
		if s.audioOnly || s.videoOnly {
			stableAVSync, avSyncStdDev, maxAVSync = nil, nil, nil
			unmatchedFlashes, unmatchedBeeps = nil, nil
		}

		out = append(out, map[string]any{
			"integrationType":    s.integrationType,
			"test":               s.test,
			"requestType":        s.requestType,
			"source":             deriveSource(s.requestType),
			"audioCodec":         s.audioCodec,
			"videoCodec":         s.videoCodec,
			"layout":             s.layout,
			"audioOnly":          s.audioOnly,
			"videoOnly":          s.videoOnly,
			"score":              s.score,
			"flashes":            flashes,
			"beeps":              beeps,
			"expectedFlashes":    s.expectedFlashes,
			"expectedBeeps":      s.expectedBeeps,
			"firstFlash":         firstFlash,
			"firstBeep":          firstBeep,
			"timeToStableVideo":  timeToStableVideo,
			"timeToStableAudio":  timeToStableAudio,
			"spacingStdDevVideo": spacingStdDevVideo,
			"spacingStdDevAudio": spacingStdDevAudio,
			"maxSpacingDevVideo": maxSpacingDevVideo,
			"maxSpacingDevAudio": maxSpacingDevAudio,
			"stableAVSync":       stableAVSync,
			"avSyncStdDev":       avSyncStdDev,
			"maxAVSync":          maxAVSync,
			"missedFlashes":      missedFlashes,
			"missedBeeps":        missedBeeps,
			"unmatchedFlashes":   unmatchedFlashes,
			"unmatchedBeeps":     unmatchedBeeps,
		})
	}

	data, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "DumpContentStats: marshal failed: %v\n", err)
		return
	}

	path := os.Getenv("AVSYNC_STATS_PATH")
	if path == "" {
		path = "/tmp/avsync-stats.json"
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "DumpContentStats: write %s failed: %v\n", path, err)
		return
	}
}

// deriveSource buckets requestType into "web" or "sdk".
func deriveSource(requestType string) string {
	switch requestType {
	case string(types.RequestTypeWeb),
		string(types.RequestTypeRoomComposite),
		string(types.RequestTypeTemplate):
		return "web"
	case string(types.RequestTypeParticipant),
		string(types.RequestTypeTrackComposite),
		string(types.RequestTypeTrack),
		string(types.RequestTypeMedia):
		return "sdk"
	}
	return ""
}

func computeContentStats(result *avsync.Result, videoWindows, audioWindows []timeWindow) contentStats {
	var s contentStats

	s.expectedFlashes = int(totalWindowDuration(videoWindows).Seconds())
	s.expectedBeeps = int(totalWindowDuration(audioWindows).Seconds())

	allFlashes, videoSpacingDevs, videoMaxDev, videoFirstStable, videoMissed :=
		perStreamCadence(flashStreams(result), videoWindows)
	s.flashes = len(allFlashes)
	if s.flashes > 0 {
		s.firstFlash = allFlashes[0]
	}
	s.timeToStableVideo = videoFirstStable
	s.spacingStdDevVideo = stdDevDuration(videoSpacingDevs)
	s.maxSpacingDevVideo = videoMaxDev
	s.missedFlashes = videoMissed

	allBeeps, audioSpacingDevs, audioMaxDev, audioFirstStable, audioMissed :=
		perStreamCadence(beepStreams(result), audioWindows)
	s.beeps = len(allBeeps)
	if s.beeps > 0 {
		s.firstBeep = allBeeps[0]
	}
	s.timeToStableAudio = audioFirstStable
	s.spacingStdDevAudio = stdDevDuration(audioSpacingDevs)
	s.maxSpacingDevAudio = audioMaxDev
	s.missedBeeps = audioMissed

	if len(allFlashes) > 0 && len(allBeeps) > 0 {
		stableStart := s.timeToStableVideo
		if s.timeToStableAudio > stableStart {
			stableStart = s.timeToStableAudio
		}
		jointFlashes := filterByWindows(filterByWindows(allFlashes, videoWindows), audioWindows)
		jointBeeps := filterByWindows(filterByWindows(allBeeps, videoWindows), audioWindows)

		// findNearest panics on an empty slice.
		if len(jointFlashes) > 0 && len(jointBeeps) > 0 {
			var stableOffsets []time.Duration
			var maxAbs time.Duration
			for _, f := range jointFlashes {
				// Use the joint-window pool so out-of-window peers don't
				// spuriously match window-edge events.
				nearest := findNearest(jointBeeps, f)
				off := f - nearest
				if absDuration(off) > maxAbs {
					maxAbs = absDuration(off)
				}
				if f >= stableStart {
					stableOffsets = append(stableOffsets, off)
				}
			}
			s.stableAVSync = medianDuration(stableOffsets)
			s.avSyncStdDev = stdDevDuration(stableOffsets)
			s.maxAVSync = maxAbs

			for _, f := range jointFlashes {
				if absDuration(f-findNearest(jointBeeps, f)) > avSyncTolerance {
					s.unmatchedFlashes++
				}
			}
			for _, b := range jointBeeps {
				if absDuration(b-findNearest(jointFlashes, b)) > avSyncTolerance {
					s.unmatchedBeeps++
				}
			}
		}
	}

	return s
}

func flashStreams(result *avsync.Result) [][]time.Duration {
	streams := make([][]time.Duration, 0, len(result.Video.Flashes))
	for _, fs := range result.Video.Flashes {
		streams = append(streams, fs)
	}
	return streams
}

func beepStreams(result *avsync.Result) [][]time.Duration {
	byPart := make(map[string][]time.Duration)
	for _, b := range result.Audio.Beeps {
		byPart[b.Participant] = append(byPart[b.Participant], b.PTS)
	}
	streams := make([][]time.Duration, 0, len(byPart))
	for _, ts := range byPart {
		streams = append(streams, ts)
	}
	return streams
}

// perStreamCadence aggregates 1Hz-cadence stats across streams: merged
// PTS list, stable-region spacing deviations and their max, earliest
// stabilization, and count of gaps ≥ missedEventGap.
func perStreamCadence(streams [][]time.Duration, windows []timeWindow) (
	merged []time.Duration,
	devs []time.Duration,
	maxDev time.Duration,
	firstStable time.Duration,
	missed int,
) {
	for _, stream := range streams {
		sorted := make([]time.Duration, len(stream))
		copy(sorted, stream)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		merged = append(merged, sorted...)

		inWindow := filterByWindows(sorted, windows)
		idx := firstStableIndex(inWindow)
		if idx > 0 && idx < len(inWindow) {
			t := inWindow[idx-1]
			if firstStable == 0 || t < firstStable {
				firstStable = t
			}
		}
		for i := idx; i < len(inWindow); i++ {
			gap := inWindow[i] - inWindow[i-1]
			if !sameWindow(inWindow[i-1], inWindow[i], windows) {
				continue
			}
			if gap >= missedEventGap {
				missed++
				continue
			}
			dev := absDuration(gap - time.Second)
			devs = append(devs, dev)
			if dev > maxDev {
				maxDev = dev
			}
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i] < merged[j] })
	return merged, devs, maxDev, firstStable, missed
}

// scoreContent collapses contentStats into 0-100. Each component is a
// weighted penalty subtracted from 100: weight × clamp((v - good) / (bad - good)).
// Thresholds err easy so any pipeline regression moves the number.
func scoreContent(s contentStats) float64 {
	score := 100.0

	score -= 25.0 * normalize(float64(s.unmatchedFlashes+s.unmatchedBeeps), 0, 5)
	score -= 20.0 * normalize(durMs(s.avSyncStdDev), 10, 50)
	score -= 15.0 * normalize(durMs(absDuration(s.stableAVSync)), 50, 300)
	score -= 15.0 * normalize(float64(s.missedFlashes+s.missedBeeps), 0, 3)

	// Cadence jitter — max of the two tracks when both present, so a
	// perfect track doesn't dilute a noisy one.
	switch {
	case s.audioOnly:
		score -= 10.0 * normalize(durMs(s.spacingStdDevAudio), 5, 50)
	case s.videoOnly:
		score -= 10.0 * normalize(durMs(s.spacingStdDevVideo), 5, 50)
	default:
		worst := durMs(s.spacingStdDevAudio)
		if v := durMs(s.spacingStdDevVideo); v > worst {
			worst = v
		}
		score -= 10.0 * normalize(worst, 5, 50)
	}

	score -= 5.0 * normalize(durMs(s.maxAVSync), 100, 500)

	maxSpacing := durMs(s.maxSpacingDevAudio)
	if v := durMs(s.maxSpacingDevVideo); v > maxSpacing {
		maxSpacing = v
	}
	score -= 5.0 * normalize(maxSpacing, 50, 200)

	tts := durMs(s.timeToStableAudio)
	if v := durMs(s.timeToStableVideo); v > tts {
		tts = v
	}
	score -= 5.0 * normalize(tts, 1000, 10000)

	if score < 0 {
		score = 0
	}
	return score
}

func normalize(value, good, bad float64) float64 {
	if value <= good {
		return 0
	}
	if value >= bad {
		return 1
	}
	return (value - good) / (bad - good)
}

func durMs(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func medianDuration(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

func stdDevDuration(values []time.Duration) time.Duration {
	if len(values) < 2 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += float64(v)
	}
	mean := sum / float64(len(values))
	var variance float64
	for _, v := range values {
		d := float64(v) - mean
		variance += d * d
	}
	variance /= float64(len(values))
	return time.Duration(math.Sqrt(variance))
}
