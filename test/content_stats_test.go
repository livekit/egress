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
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-samples/avsync"
)

// statsFromResult runs the full pipeline (quantize → computeContentStats)
// so unit tests exercise the same code path as runContentCheck.
func statsFromResult(result *avsync.Result, audioOnly, videoOnly bool) contentStats {
	return computeContentStats(quantize(result), audioOnly, videoOnly)
}

func TestNormalize(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	require.Equal(t, 0.0, normalize(5, 10, 50))
	require.Equal(t, 0.0, normalize(10, 10, 50))
	require.Equal(t, 1.0, normalize(50, 10, 50))
	require.Equal(t, 1.0, normalize(99, 10, 50))
	require.InDelta(t, 0.5, normalize(30, 10, 50), 0.0001)
}

func TestScoreContentPerfect(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	s := contentStats{
		locked:          true,
		beepCount:       10,
		flashCount:      10,
		avSyncStdDev:    1 * time.Millisecond,
		stableAVSync:    1 * time.Millisecond,
		audioJitter:     1 * time.Millisecond,
		videoJitter:     1 * time.Millisecond,
		maxAVSync:       1 * time.Millisecond,
		timeToStable:    100 * time.Millisecond,
		warmupMaxAVSync: 100 * time.Millisecond,
	}
	require.Equal(t, 100.0, scoreContent(s))
}

func TestScoreContentTerrible(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	s := contentStats{
		locked:          true,
		beepCount:       10,
		flashCount:      10,
		avSyncStdDev:    500 * time.Millisecond,
		stableAVSync:    1 * time.Second,
		audioJitter:     500 * time.Millisecond,
		videoJitter:     500 * time.Millisecond,
		maxAVSync:       2 * time.Second,
		timeToStable:    30 * time.Second,
		warmupMaxAVSync: 5 * time.Second,
	}
	require.Equal(t, 0.0, scoreContent(s))
}

func TestComputeContentStatsCleanCadence(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	// 10 beeps at integer seconds (fracLag=0), 10 flashes 50ms later
	// (per-bucket fracOffset=50ms). fractionalLag locks on the first
	// gap; jitter near 0; stableAVSync = ~50ms (video lags audio).
	var beeps []avsync.Beep
	var flashes []avsync.Flash
	for i := 0; i < 10; i++ {
		beeps = append(beeps, avsync.Beep{
			PTS:         time.Duration(i+1) * time.Second,
			Participant: "p0",
		})
		flashes = append(flashes, avsync.Flash{
			PTS:         time.Duration(i+1)*time.Second + 50*time.Millisecond,
			Participant: "p0",
		})
	}
	result := &avsync.Result{Beeps: beeps, Flashes: flashes}

	s := statsFromResult(result, false, false)

	require.Equal(t, 10, s.flashCount)
	require.Equal(t, 10, s.beepCount)
	require.Greater(t, s.timeToStable, time.Duration(0), "should detect stable region")
	require.Less(t, s.audioJitter, 5*time.Millisecond, "audio jitter should be ~0")
	require.Less(t, s.videoJitter, 5*time.Millisecond, "video jitter should be ~0")
	require.InDelta(t, float64(50*time.Millisecond), float64(s.stableAVSync), float64(5*time.Millisecond))
	require.GreaterOrEqual(t, s.score, 90.0, "clean recording should score ≥90")
}

func TestComputeContentStatsMultiParticipantAligned(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	// Three participants flashing simultaneously at 1Hz, mixed audio.
	// All events share the same fracLag (snapped to one NTP timeline).
	const fracLag = 50 * time.Millisecond
	var flashes []avsync.Flash
	for i := 0; i < 10; i++ {
		t := time.Duration(i+1)*time.Second + fracLag
		flashes = append(flashes,
			avsync.Flash{PTS: t, Participant: "p0"},
			avsync.Flash{PTS: t, Participant: "p1"},
			avsync.Flash{PTS: t, Participant: "p2"},
		)
	}
	var beeps []avsync.Beep
	for i := 0; i < 10; i++ {
		beeps = append(beeps, avsync.Beep{
			PTS:         time.Duration(i+1)*time.Second + fracLag,
			Participant: "p0",
		})
	}
	result := &avsync.Result{Beeps: beeps, Flashes: flashes}

	s := statsFromResult(result, false, false)

	require.Equal(t, 30, s.flashCount)
	require.Equal(t, 10, s.beepCount)
	require.Greater(t, s.timeToStable, time.Duration(0))
	require.Less(t, s.audioJitter, 5*time.Millisecond)
	require.Less(t, s.videoJitter, 5*time.Millisecond)
	require.Less(t, absDuration(s.stableAVSync), 5*time.Millisecond, "audio and video share the same fracLag")
	require.GreaterOrEqual(t, s.score, 90.0, "aligned multi-participant should score ≥90")
}

func TestComputeContentStatsUserExample(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	// flashes at 0.1/1.1/2.1/3.1 (all at fracOffset 100ms),
	// beeps at 0.3/1.2/2.1/3.1 (drifting fracOffsets 300/200/100/100ms).
	// Median fracOffset = 100ms. Events at 0.3 (deviates 200ms) and 1.2
	// (deviates 100ms) are outliers; the last bad event is the 1.2s beep,
	// so timeToStable lands on the next event in PTS order = 2.1s.
	result := &avsync.Result{
		Beeps: []avsync.Beep{
			{PTS: 300 * time.Millisecond, Participant: "p0"},
			{PTS: 1200 * time.Millisecond, Participant: "p0"},
			{PTS: 2100 * time.Millisecond, Participant: "p0"},
			{PTS: 3100 * time.Millisecond, Participant: "p0"},
		},
		Flashes: []avsync.Flash{
			{PTS: 100 * time.Millisecond, Participant: "p0"},
			{PTS: 1100 * time.Millisecond, Participant: "p0"},
			{PTS: 2100 * time.Millisecond, Participant: "p0"},
			{PTS: 3100 * time.Millisecond, Participant: "p0"},
		},
	}
	s := statsFromResult(result, false, false)
	require.True(t, s.locked)
	require.Equal(t, 2100*time.Millisecond, s.timeToStable)
}

func TestComputeContentStatsParticipantCompositeWindows(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	// ParticipantComposite tests use delay/unpublish/republish — audio is
	// only expected in publish windows like [8s, 14s] and [20s, 30s]. Video
	// is continuous from t=0. fractionalLag must use the video timeline
	// (which locks first) rather than the spotty audio timeline.
	var flashes []avsync.Flash
	for i := 0; i < 30; i++ {
		flashes = append(flashes, avsync.Flash{
			PTS:         time.Duration(i+1) * time.Second,
			Participant: "p0",
		})
	}
	// Audio: 1Hz inside [8s, 14s] and [20s, 30s], silent elsewhere.
	var beeps []avsync.Beep
	for _, t := range []time.Duration{8, 9, 10, 11, 12, 13, 14, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30} {
		beeps = append(beeps, avsync.Beep{
			PTS:         t * time.Second,
			Participant: "p0",
		})
	}
	result := &avsync.Result{Beeps: beeps, Flashes: flashes}

	s := statsFromResult(result, false, false)

	require.True(t, s.locked, "should lock on the continuous video stream")
	require.Less(t, s.timeToStable, 2*time.Second, "video locks within ~1s; timeToStable should not reflect the audio publish delay")
	require.Equal(t, 30, s.flashCount)
	require.Equal(t, 18, s.beepCount)
}

func TestComputeContentStatsVideoOnly(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	// Video-only: fractionalLag falls back to flashes for stabilization.
	var flashes []avsync.Flash
	for i := 0; i < 10; i++ {
		flashes = append(flashes, avsync.Flash{
			PTS:         time.Duration(i+1) * time.Second,
			Participant: "p0",
		})
	}
	result := &avsync.Result{Flashes: flashes}

	s := statsFromResult(result, false, true)

	require.Equal(t, 10, s.flashCount)
	require.Equal(t, 0, s.beepCount)
	require.Greater(t, s.timeToStable, time.Duration(0), "flash-only recording should still detect stable region")
	require.GreaterOrEqual(t, s.score, 90.0)
}

func TestComputeContentStatsEmpty(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	s := statsFromResult(&avsync.Result{}, false, false)
	require.Equal(t, 0, s.flashCount)
	require.Equal(t, 0, s.beepCount)
	require.Equal(t, time.Duration(0), s.timeToStable)
	require.Equal(t, 0.0, s.score)
}

func TestScoreContentBrokenRecording(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	// Recording that never produced a stable fracLag scores 0 so it doesn't
	// disappear from "worst score" in the aggregate.
	s := contentStats{
		beepCount:    10,
		flashCount:   10,
		timeToStable: 0,
	}
	require.Equal(t, 0.0, scoreContent(s))
}

func TestScoreContentAudioOnlyStable(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	s := contentStats{
		locked:       true,
		audioOnly:    true,
		beepCount:    10,
		timeToStable: 500 * time.Millisecond,
		audioJitter:  1 * time.Millisecond,
	}
	require.Greater(t, scoreContent(s), 90.0)
}

func TestScoreContentHalfBrokenRecording(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	// Both-track recording where audio produced events but video produced
	// none. AV-sync penalties would contribute 0, so without a guard the
	// score would be implausibly high.
	s := contentStats{
		locked:       true,
		audioOnly:    false,
		videoOnly:    false,
		beepCount:    10,
		flashCount:   0,
		timeToStable: 500 * time.Millisecond,
		audioJitter:  1 * time.Millisecond,
	}
	require.Equal(t, 0.0, scoreContent(s))
}

func TestScoreContentLockedAtZero(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	// A recording that locks at PTS=0 (first event on integer second) must
	// still score normally — the only signal for "no lock found" is the
	// `locked` flag, not `timeToStable == 0`. Reproduces the H264 sample
	// case (25fps, first flash on frame 0 → earliest=0).
	s := contentStats{
		locked:       true,
		videoOnly:    true,
		flashCount:   29,
		timeToStable: 0,
		videoJitter:  1 * time.Millisecond,
	}
	require.Greater(t, scoreContent(s), 90.0)
}
