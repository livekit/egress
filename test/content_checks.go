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
	// 1Hz cadence jitter budget. WebRTC playout (NetEQ) time-stretches
	// audio to manage jitter buffer levels, so individual beep gaps can
	// drift significantly. We collect per-gap drift and check a percentile
	// rather than failing on any single gap.
	eventSpacingTolerance = 120 * time.Millisecond
	// Encoder pipeline PTS offset between audio and video runs ~200–280ms.
	avSyncTolerance = 300 * time.Millisecond
	// Spacing-check filter: gaps outside [1s − cadenceWindow, 1s + cadenceWindow]
	// are treated as missed/extra detections, not real cadence drift; skip
	// the spacing assertion for those.
	cadenceWindow = 500 * time.Millisecond
	// WebRTC jitter buffers can occasionally shift or drop individual
	// events. Require this fraction of expected beeps/flashes to be
	// present per publisher rather than failing on any single miss.
	presenceHitRate = 0.90
	// Maximum offset the lag detector will trust between plan time and
	// recording time (Chrome warmup is ~3s; anything beyond is noise).
	maxRecordingDelay = 5 * time.Second

	layoutSpeaker       = "speaker"
	layoutSingleSpeaker = "single-speaker"
	layoutGrid          = "grid"

	regionStage = "stage"
	regionFull  = "full"
)

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

	plan := tc.plan
	if plan == nil {
		return
	}

	fracLag := fractionalLag(result)
	obs := quantize(result, dur, fracLag)

	expected := plan.expectedBeepsBySec(dur + maxRecordingDelay)
	intLag := integerLag(expected, obs.seconds)
	lag := time.Duration(intLag)*time.Second - fracLag
	if lag > maxRecordingDelay {
		intLag = 0
		lag = -fracLag
	}
	t.Logf("recording lag: %s (dur=%s)", lag, dur)

	// Recording captures plan time [lag, lag+dur]. Skip checks for any
	// planPTS within ~1s of the recording tail — the bucket may technically
	// be inside the recording, but the integer-second beep expected at that
	// planPTS often lands at the very edge and is unreliable.
	maxPlanPTS := lag + dur - 1*time.Second

	r.verifyContent(t, tc, plan, obs, intLag, maxPlanPTS)
}

// --- observation ------------------------------------------------------

// observation bins detected beeps and flashes by integer second of
// recording PTS, preserving the avsync.Beep / avsync.Flash payloads.
type observation struct {
	seconds []obsSecond
}

type obsSecond struct {
	beeps   []avsync.Beep
	flashes []avsync.Flash
}

func quantize(result *avsync.Result, dur time.Duration, fracLag time.Duration) *observation {
	if result == nil {
		return &observation{}
	}
	numSec := int64(dur/time.Second) + 2
	if numSec < 1 {
		numSec = 1
	}
	// floor((PTS - fracLag + 0.5)/1) is round-half-up on (PTS - fracLag).
	bucket := func(t time.Duration) int64 {
		adj := t - fracLag + 500*time.Millisecond
		if adj < 0 {
			return -1
		}
		return int64(adj / time.Second)
	}
	obs := &observation{seconds: make([]obsSecond, numSec)}
	for _, b := range result.Beeps {
		s := bucket(b.PTS)
		if s < 0 || s >= numSec {
			continue
		}
		obs.seconds[s].beeps = append(obs.seconds[s].beeps, b)
	}
	for _, f := range result.Flashes {
		s := bucket(f.PTS)
		if s < 0 || s >= numSec {
			continue
		}
		obs.seconds[s].flashes = append(obs.seconds[s].flashes, f)
	}
	return obs
}

// --- lag detection ----------------------------------------------------

// fractionalLag returns the sub-second component of the recording lag
func fractionalLag(result *avsync.Result) time.Duration {
	if result == nil || len(result.Beeps) == 0 {
		return 0
	}
	const lockTolerance = 1 * time.Millisecond

	pts := make(map[string][]time.Duration)
	for _, b := range result.Beeps {
		pts[b.Participant] = append(pts[b.Participant], b.PTS)
	}
	for _, beeps := range pts {
		sort.Slice(beeps, func(i, j int) bool { return beeps[i] < beeps[j] })
	}

	best := time.Minute
	for _, beeps := range pts {
		for i := 1; i < len(beeps); i++ {
			if diff := absDuration(beeps[i] - beeps[i-1] - time.Second); diff < best {
				best = diff
			}
		}
	}

	earliest := time.Duration(-1)
	for _, beeps := range pts {
		for i := 1; i < len(beeps); i++ {
			diff := absDuration(beeps[i] - beeps[i-1] - time.Second)
			if diff-best < lockTolerance {
				if earliest < 0 || beeps[i-1] < earliest {
					earliest = beeps[i-1]
				}
				break
			}
		}
	}
	if earliest < 0 {
		return 0
	}
	return earliest % time.Second
}

// expectedBeepsBySec projects the plan timeline into per-integer-second
// lists of participant names that may beep at that plan time — both
// required slots and optional (grace) slots. Used as the alignment
// target for lag detection: optional slots are part of the publisher's
// footprint and must not be treated as "no content" or integerLag will
// shift past them.
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

// integerLag scores each candidate lag (0..maxIntLag) and returns the
// best. Score is the count of buckets where actual and expected agree:
//   - actual bucket has a beep whose participant is in expected[i+lag]
//   - both actual bucket and expected[i+lag] are empty (silent matches
//     silent — keeps the lag from drifting past long forbidden zones
//     just because the remaining content fits more buckets at a higher
//     lag)
//
// Smallest lag wins ties.
func integerLag(expected [][]string, actual []obsSecond) int64 {
	const maxIntLag = int64(5)
	var bestLag int64
	bestScore := -1
	for lag := int64(0); lag <= maxIntLag; lag++ {
		score := 0
		for i := int64(0); i < int64(len(actual)); i++ {
			ei := i + lag
			if ei >= int64(len(expected)) {
				break
			}
			if len(actual[i].beeps) == 0 {
				if len(expected[ei]) == 0 {
					score++
				}
				continue
			}
			for _, b := range actual[i].beeps {
				for _, name := range expected[ei] {
					if b.Participant == name {
						score++
						break
					}
				}
			}
		}
		if score > bestScore {
			bestScore = score
			bestLag = lag
		}
	}
	return bestLag
}

func (r *Runner) verifyContent(t *testing.T, tc *testCase, plan *Plan, obs *observation, intLag int64, maxPlanPTS time.Duration) {
	t.Helper()

	var issues []string
	addIssue := func(format string, args ...any) {
		issues = append(issues, fmt.Sprintf(format, args...))
	}

	var avSyncOffsets []time.Duration
	var beepCadenceDrifts []time.Duration
	var flashCadenceDrifts []time.Duration
	var stageMismatches []string

	lastBeepPTS := make(map[string]time.Duration)
	lastFlashPTS := make(map[string]time.Duration)
	seenInSecondary := make(map[string]bool)
	beepRequired := make(map[string]int)
	beepMissing := make(map[string]int)
	flashRequired := make(map[string]int)
	flashMissing := make(map[string]int)

	isStageRegion := func(region string) bool {
		return region == regionStage || region == regionFull
	}
	hasSecondaryRegions := tc.multiParticipant && (tc.layout == layoutSpeaker || tc.layout == layoutGrid)

	warmupCutoff := time.Duration(intLag)*time.Second + publishSettling

	for sec, secData := range obs.seconds {
		// bucket sec holds beeps emitted at plan-time (sec + intLag).
		planPTS := time.Duration(int64(sec)+intLag) * time.Second
		if planPTS > maxPlanPTS {
			continue
		}
		inWarmup := planPTS < warmupCutoff

		beepsByPub := make(map[string][]avsync.Beep)
		for _, b := range secData.beeps {
			beepsByPub[b.Participant] = append(beepsByPub[b.Participant], b)
		}
		flashesByPub := make(map[string][]avsync.Flash)
		for _, f := range secData.flashes {
			flashesByPub[f.Participant] = append(flashesByPub[f.Participant], f)
			if !isStageRegion(f.Region) {
				seenInSecondary[f.Participant] = true
			}
		}

		for _, pub := range plan.publishers {
			gotBeeps := beepsByPub[pub.name]
			beepVerdict := pub.expectsBeep(planPTS)
			if inWarmup && beepVerdict == required {
				beepVerdict = optional
			}
			switch beepVerdict {
			case required:
				beepRequired[pub.name]++
				if len(gotBeeps) == 0 {
					beepMissing[pub.name]++
				}
			case forbidden:
				if len(gotBeeps) > 0 {
					addIssue("@%s unexpected beep from %s", planPTS, pub.name)
				}
			}

			// Cadence: gap to previous required-and-observed beep should
			// be ~1s. Gaps outside cadenceWindow imply a missed event
			// (already flagged above) and are skipped. Within the window,
			// collect the drift for a percentile check at the end — NetEQ
			// can time-stretch playout at any point, so individual gaps
			// are unreliable.
			if beepVerdict == required && len(gotBeeps) > 0 {
				if last, ok := lastBeepPTS[pub.name]; ok {
					gap := gotBeeps[0].PTS - last
					diff := absDuration(gap - time.Second)
					if diff <= cadenceWindow {
						beepCadenceDrifts = append(beepCadenceDrifts, diff)
					}
				}
				lastBeepPTS[pub.name] = gotBeeps[len(gotBeeps)-1].PTS
			}

			// Channel routing: detected beeps should match the test's
			// expected channel for this participant.
			if expCh, expIn := resolveExpectedChannel(tc, pub.name); expIn {
				for _, b := range gotBeeps {
					if b.Channel != expCh {
						addIssue("@%s beep from %s on wrong channel: got %d, want %d",
							planPTS, pub.name, b.Channel, expCh)
					}
				}
			}

			gotFlashes := flashesByPub[pub.name]
			flashVerdict := pub.expectsFlash(planPTS)
			if inWarmup && flashVerdict == required {
				flashVerdict = optional
			}
			switch flashVerdict {
			case required:
				flashRequired[pub.name]++
				if len(gotFlashes) == 0 {
					flashMissing[pub.name]++
				}
			case forbidden:
				if len(gotFlashes) > 0 {
					addIssue("@%s unexpected flash from %s", planPTS, pub.name)
				}
			}
			if flashVerdict == required && len(gotFlashes) > 0 {
				if last, ok := lastFlashPTS[pub.name]; ok {
					gap := gotFlashes[0].PTS - last
					diff := absDuration(gap - time.Second)
					if diff <= cadenceWindow {
						flashCadenceDrifts = append(flashCadenceDrifts, diff)
					}
				}
				lastFlashPTS[pub.name] = gotFlashes[len(gotFlashes)-1].PTS
			}

			// AV-sync: measure tight pair only when both flash and beep
			// were strictly required this second — any grace would skew
			// the offset.
			if beepVerdict == required && flashVerdict == required &&
				len(gotBeeps) > 0 && len(gotFlashes) > 0 {
				minOff := absDuration(gotFlashes[0].PTS - gotBeeps[0].PTS)
				for _, f := range gotFlashes {
					for _, b := range gotBeeps {
						if d := absDuration(f.PTS - b.PTS); d < minOff {
							minOff = d
						}
					}
				}
				avSyncOffsets = append(avSyncOffsets, minOff)
			}
		}

		// Stage attribution: in speaker / single-speaker layouts, the
		// participant rendered on stage must match the active speaker
		// (unless we're inside a transition window — activeSpeaker
		// returns "" then, or inside the recording warmup before chrome
		// has settled on a speaker).
		if !inWarmup && (tc.layout == layoutSpeaker || tc.layout == layoutSingleSpeaker) {
			if speaker := plan.activeSpeaker(planPTS); speaker != "" {
				for _, f := range secData.flashes {
					if isStageRegion(f.Region) && f.Participant != "" && f.Participant != speaker {
						stageMismatches = append(stageMismatches, fmt.Sprintf("@%s stage shows %s, expected %s", planPTS, f.Participant, speaker))
					}
				}
			}
		}
	}

	// Every publisher with video should have appeared in some secondary
	// (thumb / cell) region at least once during the run.
	if hasSecondaryRegions {
		for _, pub := range plan.publishers {
			hasVideo := false
			for _, e := range pub.video {
				if e.kind == eventPublish {
					hasVideo = true
					break
				}
			}
			if hasVideo && !seenInSecondary[pub.name] {
				addIssue("%s never visible in any %s region", pub.name, secondaryRegionLabel(tc.layout))
			}
		}
	}

	// Stage attribution: log mismatches for diagnostics but don't fail.
	// Chrome's speaker layout rendering depends on WebRTC subscription
	// order and active speaker detection timing, which vary between runs.
	if len(stageMismatches) > 0 {
		t.Logf("stage-attribution: %d mismatches", len(stageMismatches))
		for _, m := range stageMismatches {
			t.Logf("  %s", m)
		}
	}

	// Beep cadence: use p80 to tolerate NetEQ time-stretching bursts.
	if len(beepCadenceDrifts) > 2 {
		sort.Slice(beepCadenceDrifts, func(i, j int) bool { return beepCadenceDrifts[i] < beepCadenceDrifts[j] })
		drift := beepCadenceDrifts[percentileIdx(len(beepCadenceDrifts))]
		t.Logf("beep-cadence: p80=%s over %d gaps", drift, len(beepCadenceDrifts))
		if drift > eventSpacingTolerance {
			addIssue("beep cadence p80=%s exceeds %s tolerance", drift, eventSpacingTolerance)
		}
	}

	// Beep/flash presence: require presenceHitRate of expected events per
	// publisher. WebRTC jitter buffers can shift or drop individual events.
	// With few samples the hit rate is too coarse (each miss is a large %
	// swing), so allow up to 1 miss unconditionally.
	checkPresence := func(kind, name string, req, miss int) {
		if req == 0 {
			return
		}
		rate := float64(req-miss) / float64(req)
		t.Logf("%s-presence %s: %d/%d (%.0f%%)", kind, name, req-miss, req, rate*100)
		if miss > 1 && rate < presenceHitRate {
			addIssue("%s hit rate for %s: %.0f%% < %.0f%% required (%d missing out of %d)",
				kind, name, rate*100, presenceHitRate*100, miss, req)
		}
	}
	for _, pub := range plan.publishers {
		checkPresence("beep", pub.name, beepRequired[pub.name], beepMissing[pub.name])
		checkPresence("flash", pub.name, flashRequired[pub.name], flashMissing[pub.name])
	}

	// Flash cadence: same percentile approach.
	if len(flashCadenceDrifts) > 2 {
		sort.Slice(flashCadenceDrifts, func(i, j int) bool { return flashCadenceDrifts[i] < flashCadenceDrifts[j] })
		drift := flashCadenceDrifts[percentileIdx(len(flashCadenceDrifts))]
		t.Logf("flash-cadence: p80=%s over %d gaps", drift, len(flashCadenceDrifts))
		if drift > eventSpacingTolerance {
			addIssue("flash cadence p80=%s exceeds %s tolerance", drift, eventSpacingTolerance)
		}
	}

	if len(avSyncOffsets) > 2 {
		sort.Slice(avSyncOffsets, func(i, j int) bool { return avSyncOffsets[i] < avSyncOffsets[j] })
		offset := avSyncOffsets[percentileIdx(len(avSyncOffsets))]
		t.Logf("av-sync: p80=%s over %d strict-pair buckets", offset, len(avSyncOffsets))
		if offset > avSyncTolerance {
			addIssue("av-sync p80=%s exceeds %s tolerance", offset, avSyncTolerance)
		}
	}

	if len(issues) > 0 {
		require.Empty(t, issues, "content verification failed (%d issues):\n  %s",
			len(issues), strings.Join(issues, "\n  "))
	}
}

func secondaryRegionLabel(layout string) string {
	if layout == layoutGrid {
		return "cell"
	}
	return "thumb"
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

// --- general helpers ---

// percentileIdx returns the index for a p80 lookup in a sorted slice of
// length n. For small slices (< 10) p80 degenerates to the max, so fall
// back to p50 to avoid letting a single outlier dominate.
func percentileIdx(n int) int {
	if n < 10 {
		return n / 2
	}
	return (n * 8) / 10
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
