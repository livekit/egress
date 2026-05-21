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

package cadence

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-samples/avsync"
)

// statsFromResult runs the full pipeline (Quantize → Compute) so unit
// tests exercise the same code path as integration callers.
func statsFromResult(result *avsync.Result, audioOnly, videoOnly bool) Stats {
	return Compute(Quantize(result), audioOnly, videoOnly)
}

func TestNormalize(t *testing.T) {
	require.Equal(t, 0.0, normalize(5, 10, 50))
	require.Equal(t, 0.0, normalize(10, 10, 50))
	require.Equal(t, 1.0, normalize(50, 10, 50))
	require.Equal(t, 1.0, normalize(99, 10, 50))
	require.InDelta(t, 0.5, normalize(30, 10, 50), 0.0001)
}

func TestScorePerfect(t *testing.T) {
	s := Stats{
		Locked:       true,
		BeepCount:    10,
		FlashCount:   10,
		AVSyncStdDev: 1 * time.Millisecond,
		StableAVSync: 1 * time.Millisecond,
		AudioJitter:  1 * time.Millisecond,
		VideoJitter:  1 * time.Millisecond,
		MaxAVSync:    1 * time.Millisecond,
		TimeToStable: 100 * time.Millisecond,
	}
	require.Equal(t, 100.0, score(s))
}

func TestScoreTerrible(t *testing.T) {
	s := Stats{
		Locked:       true,
		BeepCount:    10,
		FlashCount:   10,
		AVSyncStdDev: 500 * time.Millisecond,
		StableAVSync: 1 * time.Second,
		AudioJitter:  500 * time.Millisecond,
		VideoJitter:  500 * time.Millisecond,
		MaxAVSync:    2 * time.Second,
		TimeToStable: 30 * time.Second,
	}
	require.Equal(t, 0.0, score(s))
}

func TestComputeCleanCadence(t *testing.T) {
	// 10 beeps at integer seconds (fracLag=0), 10 flashes 50ms later
	// (per-bucket fracOffset=50ms). Quantize locks immediately;
	// jitter near 0; StableAVSync = ~50ms (video lags audio).
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

	require.Equal(t, 10, s.FlashCount)
	require.Equal(t, 10, s.BeepCount)
	require.Greater(t, s.TimeToStable, time.Duration(0), "should detect stable region")
	require.Less(t, s.AudioJitter, 5*time.Millisecond, "audio jitter should be ~0")
	require.Less(t, s.VideoJitter, 5*time.Millisecond, "video jitter should be ~0")
	require.InDelta(t, float64(50*time.Millisecond), float64(s.StableAVSync), float64(5*time.Millisecond))
	require.GreaterOrEqual(t, s.Score, 90.0, "clean recording should score ≥90")
}

func TestComputeMultiParticipantAligned(t *testing.T) {
	// Three participants flashing simultaneously at 1Hz, mixed audio.
	// All events share the same fracLag (snapped to one NTP timeline).
	const fracLag = 50 * time.Millisecond
	var flashes []avsync.Flash
	for i := 0; i < 10; i++ {
		ts := time.Duration(i+1)*time.Second + fracLag
		flashes = append(flashes,
			avsync.Flash{PTS: ts, Participant: "p0"},
			avsync.Flash{PTS: ts, Participant: "p1"},
			avsync.Flash{PTS: ts, Participant: "p2"},
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

	require.Equal(t, 30, s.FlashCount)
	require.Equal(t, 10, s.BeepCount)
	require.Greater(t, s.TimeToStable, time.Duration(0))
	require.Less(t, s.AudioJitter, 5*time.Millisecond)
	require.Less(t, s.VideoJitter, 5*time.Millisecond)
	require.Less(t, absDuration(s.StableAVSync), 5*time.Millisecond, "audio and video share the same fracLag")
	require.GreaterOrEqual(t, s.Score, 90.0, "aligned multi-participant should score ≥90")
}

func TestComputeMultiParticipantStaggered(t *testing.T) {
	// Three participants beeping at different sub-second offsets — the
	// failure mode we hit on multi/RoomComposite before per-participant
	// outlier checks. Each participant's beeps are clean (no jitter), but
	// the cross-participant offset is ~150ms total. Should still lock.
	var beeps []avsync.Beep
	for i := 0; i < 10; i++ {
		base := time.Duration(i+1) * time.Second
		beeps = append(beeps,
			avsync.Beep{PTS: base + 100*time.Millisecond, Participant: "p0"},
			avsync.Beep{PTS: base + 200*time.Millisecond, Participant: "p1"},
			avsync.Beep{PTS: base + 250*time.Millisecond, Participant: "p2"},
		)
	}
	result := &avsync.Result{Beeps: beeps}

	s := statsFromResult(result, true, false)

	require.True(t, s.Locked, "per-participant outlier checks should let staggered participants lock")
	require.Greater(t, s.TimeToStable, time.Duration(0))
}

func TestComputeVideoOnly(t *testing.T) {
	var flashes []avsync.Flash
	for i := 0; i < 10; i++ {
		flashes = append(flashes, avsync.Flash{
			PTS:         time.Duration(i+1) * time.Second,
			Participant: "p0",
		})
	}
	result := &avsync.Result{Flashes: flashes}

	s := statsFromResult(result, false, true)

	require.Equal(t, 10, s.FlashCount)
	require.Equal(t, 0, s.BeepCount)
	require.Greater(t, s.TimeToStable, time.Duration(0), "flash-only recording should still detect stable region")
	require.GreaterOrEqual(t, s.Score, 90.0)
}

func TestComputeEmpty(t *testing.T) {
	s := statsFromResult(&avsync.Result{}, false, false)
	require.Equal(t, 0, s.FlashCount)
	require.Equal(t, 0, s.BeepCount)
	require.Equal(t, time.Duration(0), s.TimeToStable)
	require.Equal(t, 0.0, s.Score)
}

func TestScoreBrokenRecording(t *testing.T) {
	// Recording that never produced a stable fracLag scores 0 so it
	// doesn't disappear from "worst score" in the aggregate.
	s := Stats{
		BeepCount:    10,
		FlashCount:   10,
		TimeToStable: 0,
	}
	require.Equal(t, 0.0, score(s))
}

func TestScoreAudioOnlyStable(t *testing.T) {
	s := Stats{
		Locked:       true,
		AudioOnly:    true,
		BeepCount:    10,
		TimeToStable: 500 * time.Millisecond,
		AudioJitter:  1 * time.Millisecond,
	}
	require.Greater(t, score(s), 90.0)
}

func TestScoreHalfBrokenRecording(t *testing.T) {
	// Both-track recording where audio produced events but video produced
	// none. AV-sync penalties would contribute 0, so without a guard the
	// score would be implausibly high.
	s := Stats{
		Locked:       true,
		AudioOnly:    false,
		VideoOnly:    false,
		BeepCount:    10,
		FlashCount:   0,
		TimeToStable: 500 * time.Millisecond,
		AudioJitter:  1 * time.Millisecond,
	}
	require.Equal(t, 0.0, score(s))
}

func TestScoreLockedAtZero(t *testing.T) {
	// A recording that locks at PTS=0 (first event on integer second) must
	// still score normally — the only signal for "no lock found" is the
	// Locked flag, not TimeToStable == 0. Reproduces the H264 sample case
	// (25fps, first flash on frame 0 → earliest=0).
	s := Stats{
		Locked:       true,
		VideoOnly:    true,
		FlashCount:   29,
		TimeToStable: 0,
		VideoJitter:  1 * time.Millisecond,
	}
	require.Greater(t, score(s), 90.0)
}

func TestDeriveSource(t *testing.T) {
	require.Equal(t, "web", DeriveSource("room_composite"))
	require.Equal(t, "web", DeriveSource("web"))
	require.Equal(t, "web", DeriveSource("template"))
	require.Equal(t, "sdk", DeriveSource("participant"))
	require.Equal(t, "sdk", DeriveSource("track_composite"))
	require.Equal(t, "sdk", DeriveSource("track"))
	require.Equal(t, "sdk", DeriveSource("media"))
	require.Equal(t, "", DeriveSource("unknown"))
}
