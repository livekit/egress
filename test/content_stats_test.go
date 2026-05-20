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

func TestStableStartIndex(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	cases := []struct {
		name string
		pts  []time.Duration
		want int // -1 means "never stable"
	}{
		{
			name: "empty",
			pts:  nil,
			want: -1,
		},
		{
			name: "too few events",
			pts:  []time.Duration{1 * time.Second, 2 * time.Second},
			want: -1,
		},
		{
			name: "clean cadence from start",
			pts: []time.Duration{
				1 * time.Second, 2 * time.Second, 3 * time.Second,
				4 * time.Second, 5 * time.Second,
			},
			want: 0,
		},
		{
			name: "bumpy warmup then stable",
			pts: []time.Duration{
				100 * time.Millisecond,
				300 * time.Millisecond,
				1500 * time.Millisecond,
				2500 * time.Millisecond,
				3500 * time.Millisecond,
				4500 * time.Millisecond,
				5500 * time.Millisecond,
			},
			want: 2, // first index where next 3 gaps are all ~1s
		},
		{
			name: "permanently erratic",
			pts: []time.Duration{
				100 * time.Millisecond,
				900 * time.Millisecond,
				2100 * time.Millisecond,
				2400 * time.Millisecond,
				4000 * time.Millisecond,
			},
			want: -1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, stableStartIndex(tc.pts))
		})
	}
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
		avSyncStdDev:      1 * time.Millisecond,
		stableAVSync:      1 * time.Millisecond,
		audioJitter:       1 * time.Millisecond,
		videoJitter:       1 * time.Millisecond,
		maxAVSync:         1 * time.Millisecond,
		timeToStable:      100 * time.Millisecond,
		timeToStableVideo: 100 * time.Millisecond,
		timeToStableAudio: 100 * time.Millisecond,
		warmupMaxAVSync:   100 * time.Millisecond,
	}
	require.Equal(t, 100.0, scoreContent(s))
}

func TestScoreContentTerrible(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	s := contentStats{
		avSyncStdDev:      500 * time.Millisecond,
		stableAVSync:      1 * time.Second,
		audioJitter:       500 * time.Millisecond,
		videoJitter:       500 * time.Millisecond,
		maxAVSync:         2 * time.Second,
		timeToStable:      30 * time.Second,
		timeToStableVideo: 30 * time.Second,
		timeToStableAudio: 30 * time.Second,
		warmupMaxAVSync:   5 * time.Second,
	}
	require.Equal(t, 0.0, scoreContent(s))
}

func TestComputeContentStatsCleanCadence(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	// Synthetic clean recording: 10 beeps and 10 flashes, exactly 1s apart,
	// 50ms video-after-audio offset (positive AV-sync).
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

	s := computeContentStats(result)

	require.Equal(t, 10, s.flashes)
	require.Equal(t, 10, s.beeps)
	require.Less(t, s.audioJitter, 5*time.Millisecond, "audio jitter should be ~0")
	require.Less(t, s.videoJitter, 5*time.Millisecond, "video jitter should be ~0")
	require.InDelta(t, float64(50*time.Millisecond), float64(s.stableAVSync), float64(5*time.Millisecond))
	require.GreaterOrEqual(t, s.score, 90.0, "clean recording should score ≥90")
}

func TestComputeContentStatsEmpty(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	s := computeContentStats(&avsync.Result{})
	require.Equal(t, 0, s.flashes)
	require.Equal(t, 0, s.beeps)
	require.Equal(t, time.Duration(0), s.timeToStable)
}

func TestScoreContentBrokenRecording(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	// Both-track recording that never stabilized — score must be 0 so the
	// row doesn't disappear from "worst score" in the aggregate.
	s := contentStats{
		audioOnly:    false,
		videoOnly:    false,
		timeToStable: 0,
	}
	require.Equal(t, 0.0, scoreContent(s))
}

func TestScoreContentAudioOnlyStable(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	// Audio-only recording that did stabilize should not hit the broken guard.
	s := contentStats{
		audioOnly:         true,
		timeToStable:      500 * time.Millisecond,
		timeToStableAudio: 500 * time.Millisecond,
		audioJitter:       1 * time.Millisecond,
	}
	require.Greater(t, scoreContent(s), 90.0)
}

func TestScoreContentHalfBrokenRecording(t *testing.T) {
	if os.Getenv("INTEGRATION_TYPE") != "" {
		t.Skip()
	}
	// Both-track recording where audio stabilized but video produced
	// nothing. AV-sync penalties contribute 0 in this case, so without
	// the per-side guard the score would be implausibly high.
	s := contentStats{
		audioOnly:         false,
		videoOnly:         false,
		timeToStableAudio: 500 * time.Millisecond,
		timeToStableVideo: 0,
		timeToStable:      500 * time.Millisecond,
		audioJitter:       1 * time.Millisecond,
	}
	require.Equal(t, 0.0, scoreContent(s))
}
