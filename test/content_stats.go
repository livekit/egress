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
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/linkdata/deadlock"

	"github.com/livekit/media-samples/avsync"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/pkg/types"
)

// contentStats holds the metrics published per output (file/stream/segments).
// One row is appended to the global allStats slice per call to recordContentStats.
type contentStats struct {
	// Identity (labels, no math).
	integrationType string
	test            string
	requestType     string
	source          string // web or sdk
	output          string // file, stream, segments
	format          string // ogg, rtmp, hls, etc.
	audioCodec      string
	videoCodec      string
	layout          string
	audioOnly       bool
	videoOnly       bool
	tracks          int

	// Sanity counters (in JSON/log only, not in summary tables).
	flashCount int
	beepCount  int

	// Stabilization.
	locked          bool // observation produced a stable region
	timeToStable    time.Duration
	warmupMaxAVSync time.Duration

	// Post-stable steady state.
	audioJitter  time.Duration
	videoJitter  time.Duration
	stableAVSync time.Duration // signed; positive = video lags audio
	avSyncStdDev time.Duration
	maxAVSync    time.Duration

	// Composite.
	score float64
}

// allStats collects one row per recordContentStats call. DumpContentStats
// reads it at end-of-test (registered via t.Cleanup in TestEgress).
var (
	allStats   []contentStats
	allStatsMu deadlock.Mutex
)

// recordContentStats computes stats from the quantized observation and
// recording-lag values produced upstream by fractionalLag/quantize, logs
// the row (for Datadog), and appends to allStats. Safe for concurrent calls.
func recordContentStats(tc *testCase, obs *observation, output, format string) {
	s := computeContentStats(obs, tc.audioOnly, tc.videoOnly)

	s.integrationType = os.Getenv("INTEGRATION_TYPE")
	s.test = tc.name
	s.requestType = string(tc.requestType)
	s.source = deriveSource(string(tc.requestType))
	s.output = output
	s.format = format
	s.audioCodec = string(tc.audioCodec)
	s.videoCodec = string(tc.videoCodec)
	s.layout = tc.layout
	s.tracks = inputTrackCount(tc)

	logger.Infow("avsync stats",
		"test", s.test,
		"requestType", s.requestType,
		"source", s.source,
		"output", s.output,
		"format", s.format,
		"audioCodec", s.audioCodec,
		"videoCodec", s.videoCodec,
		"layout", s.layout,
		"audioOnly", s.audioOnly,
		"videoOnly", s.videoOnly,
		"tracks", s.tracks,
		"score", s.score,
		"flashes", s.flashCount,
		"beeps", s.beepCount,
		"timeToStable", s.timeToStable,
		"warmupMaxAVSync", s.warmupMaxAVSync,
		"audioJitter", s.audioJitter,
		"videoJitter", s.videoJitter,
		"avSync", s.stableAVSync,
		"avSyncStdDev", s.avSyncStdDev,
		"maxAVSync", s.maxAVSync,
	)

	allStatsMu.Lock()
	allStats = append(allStats, s)
	allStatsMu.Unlock()
}

// computeContentStats derives the metric set from the quantized
// observation. fracLag is the recording's sub-second offset; earliest is
// the PTS of the first event where the 1Hz cadence locked (= timeToStable).
// Both come from fractionalLag.
//
// Within a bucket, every event's fracOffset is measured as
// `event.PTS - (bucket*1s + fracLag)`. For a perfectly stable beep this
// is 0 by construction (fracLag is inferred from the beep timeline);
// flashes' fracOffsets relative to the same reference reveal the AV-sync
// gap and its drift across the recording.
func computeContentStats(obs *observation, audioOnly, videoOnly bool) contentStats {
	var s contentStats
	s.audioOnly = audioOnly
	s.videoOnly = videoOnly

	if obs == nil {
		s.score = math.Round(scoreContent(s)*10) / 10
		return s
	}

	for _, sec := range obs.buckets {
		for _, beeps := range sec.beeps {
			s.beepCount += len(beeps)
		}
		for _, flashes := range sec.flashes {
			s.flashCount += len(flashes)
		}
	}

	stableBucket, timeToStable := obs.timeToStabilize()
	s.locked = stableBucket >= 0
	s.timeToStable = timeToStable
	if !s.locked {
		s.score = math.Round(scoreContent(s)*10) / 10
		return s
	}

	var audioFracs, videoFracs []time.Duration
	var stableDiffs, warmupDiffs []time.Duration
	for i, sec := range obs.buckets {
		if len(sec.beeps) == 0 && len(sec.flashes) == 0 {
			continue
		}
		secStart := time.Duration(i)*time.Second + obs.center
		var aFracs, vFracs []time.Duration
		for _, beeps := range sec.beeps {
			for _, b := range beeps {
				aFracs = append(aFracs, b.PTS-secStart)
			}
		}
		for _, flashes := range sec.flashes {
			for _, f := range flashes {
				vFracs = append(vFracs, f.PTS-secStart)
			}
		}

		isStable := i >= stableBucket
		if isStable {
			audioFracs = append(audioFracs, aFracs...)
			videoFracs = append(videoFracs, vFracs...)
		}
		if len(aFracs) > 0 && len(vFracs) > 0 {
			diff := medianDuration(vFracs) - medianDuration(aFracs)
			if isStable {
				stableDiffs = append(stableDiffs, diff)
			} else {
				warmupDiffs = append(warmupDiffs, diff)
			}
		}
	}

	s.audioJitter = stdDevDuration(audioFracs)
	s.videoJitter = stdDevDuration(videoFracs)
	if len(audioFracs) > 0 && len(videoFracs) > 0 {
		s.stableAVSync = medianDuration(videoFracs) - medianDuration(audioFracs)
	}
	if len(stableDiffs) > 0 {
		s.avSyncStdDev = stdDevDuration(stableDiffs)
		for _, d := range stableDiffs {
			if a := absDuration(d); a > s.maxAVSync {
				s.maxAVSync = a
			}
		}
	}
	for _, d := range warmupDiffs {
		if a := absDuration(d); a > s.warmupMaxAVSync {
			s.warmupMaxAVSync = a
		}
	}

	s.score = math.Round(scoreContent(s)*10) / 10
	return s
}

// scoreContent collapses contentStats into a 0–100 score. Weights and
// thresholds come from the design spec; see the spec for rationale.
//
// Returns 0 if the recording produced no usable cadence data (no stable
// region in either track that was expected) — otherwise a broken recording
// with zero events would silently score 100 and disappear from the
// "worst score" column of the aggregate table.
func scoreContent(s contentStats) float64 {
	if !s.locked {
		// Observation never produced a stable region. Score 0 so the row
		// doesn't disappear from the aggregate's "worst score" column.
		// (timeToStable == 0 alone is NOT a broken signal: it can mean
		// the recording locked from the very first event, e.g., H264
		// sample at 25fps with first flash on frame 0.)
		return 0
	}
	if !s.audioOnly && !s.videoOnly && (s.beepCount == 0 || s.flashCount == 0) {
		// Both tracks expected but one produced no events; the AV-sync
		// penalties contribute 0 (they only run when both sides have
		// stable events), so the formula would score a half-broken
		// recording implausibly well.
		return 0
	}

	score := 100.0

	score -= 25.0 * normalize(durMs(s.avSyncStdDev), 10, 50)
	score -= 20.0 * normalize(durMs(absDuration(s.stableAVSync)), 50, 300)
	score -= 10.0 * normalize(durMs(s.audioJitter), 5, 50)
	score -= 10.0 * normalize(durMs(s.videoJitter), 5, 50)
	score -= 10.0 * normalize(durMs(s.maxAVSync), 100, 500)
	score -= 10.0 * normalize(durSec(s.timeToStable), 1, 10)
	score -= 15.0 * normalize(durMs(s.warmupMaxAVSync), 500, 2000)

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

// inputTrackCount returns the input track count for a test case:
// participants × (audio + video tracks per participant).
func inputTrackCount(tc *testCase) int {
	participants := 1
	if tc.multiParticipant {
		participants = len(avsync.AllParticipants) // 3
	}
	perPart := 2
	if tc.audioOnly || tc.videoOnly {
		perPart = 1
	}
	return participants * perPart
}

func medianDuration(v []time.Duration) time.Duration {
	if len(v) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), v...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func stdDevDuration(v []time.Duration) time.Duration {
	if len(v) < 2 {
		return 0
	}
	var sum float64
	for _, d := range v {
		sum += float64(d)
	}
	mean := sum / float64(len(v))
	var sq float64
	for _, d := range v {
		diff := float64(d) - mean
		sq += diff * diff
	}
	return time.Duration(math.Sqrt(sq / float64(len(v)-1)))
}

// absDuration is defined in content_checks.go.

func durMs(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func durSec(d time.Duration) float64 {
	return float64(d) / float64(time.Second)
}

// DumpContentStats writes allStats to AVSYNC_STATS_PATH (default
// /tmp/avsync-stats.json). Intended to be invoked from t.Cleanup in TestEgress
// so it runs once after every output has been processed.
//
// Inapplicable metrics (e.g., video stats on audio-only outputs) serialize
// as JSON null so the rendered table can show "N/A" instead of misleading 0.
func DumpContentStats() {
	allStatsMu.Lock()
	defer allStatsMu.Unlock()

	out := make([]map[string]any, 0, len(allStats))
	for _, s := range allStats {
		// Durations are emitted as float64 seconds (not duration strings) so
		// the jq renderer doesn't have to parse Go's multi-unit duration
		// format (e.g., "1m30s"). The structured log line above keeps
		// time.Duration values for human readability.
		var (
			flashes         any = s.flashCount
			videoJitter     any = s.videoJitter.Seconds()
			beeps           any = s.beepCount
			audioJitter     any = s.audioJitter.Seconds()
			warmupMaxAVSync any = s.warmupMaxAVSync.Seconds()
			stableAVSync    any = s.stableAVSync.Seconds()
			avSyncStdDev    any = s.avSyncStdDev.Seconds()
			maxAVSync       any = s.maxAVSync.Seconds()
		)
		if s.audioOnly {
			flashes, videoJitter = nil, nil
		}
		if s.videoOnly {
			beeps, audioJitter = nil, nil
		}
		if s.audioOnly || s.videoOnly {
			warmupMaxAVSync, stableAVSync, avSyncStdDev, maxAVSync = nil, nil, nil, nil
		}

		out = append(out, map[string]any{
			"integrationType": s.integrationType,
			"test":            s.test,
			"requestType":     s.requestType,
			"source":          s.source,
			"output":          s.output,
			"format":          s.format,
			"audioCodec":      s.audioCodec,
			"videoCodec":      s.videoCodec,
			"layout":          s.layout,
			"audioOnly":       s.audioOnly,
			"videoOnly":       s.videoOnly,
			"tracks":          s.tracks,
			"flashes":         flashes,
			"beeps":           beeps,
			"score":           s.score,
			"timeToStable":    s.timeToStable.Seconds(),
			"warmupMaxAVSync": warmupMaxAVSync,
			"audioJitter":     audioJitter,
			"videoJitter":     videoJitter,
			"avSync":          stableAVSync,
			"avSyncStdDev":    avSyncStdDev,
			"maxAVSync":       maxAVSync,
		})
	}

	data, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "DumpContentStats: marshal failed: %v\n", err)
		return
	}

	target := os.Getenv("AVSYNC_STATS_PATH")
	if target == "" {
		target = "/tmp/avsync-stats.json"
	}
	if err := os.WriteFile(target, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "DumpContentStats: write %s failed: %v\n", target, err)
		return
	}
}

// formatFromFileName returns a lowercase extension without the leading dot,
// e.g. "mp4" for "/tmp/recording.mp4". Used by file-output callers.
func formatFromFileName(name string) string {
	ext := strings.TrimPrefix(path.Ext(name), ".")
	return strings.ToLower(ext)
}

// formatFromStreamURL extracts the protocol from a stream URL, e.g. "rtmp"
// from "rtmp://host/app/stream". Returns "" if unrecognized.
func formatFromStreamURL(url string) string {
	if i := strings.Index(url, "://"); i > 0 {
		return strings.ToLower(url[:i])
	}
	return ""
}
