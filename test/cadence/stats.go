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
	"math"
	"sort"
	"time"

	"github.com/livekit/egress/pkg/types"
)

// Stats holds the metrics published per output (file/stream/segments).
// Identity fields are set by the caller (test glue); computed fields
// are set by Compute.
type Stats struct {
	// Identity
	IntegrationType string
	Test            string
	RequestType     string
	Source          string
	Output          string
	Format          string
	AudioCodec      string
	VideoCodec      string
	Layout          string
	AudioOnly       bool
	VideoOnly       bool
	Tracks          int

	// Sanity counters
	FlashCount int
	BeepCount  int

	// Stabilization
	Locked       bool
	TimeToStable time.Duration

	// Post-stable steady state
	AudioJitter  time.Duration
	VideoJitter  time.Duration
	StableAVSync time.Duration // signed; positive = video lags audio
	AVSyncStdDev time.Duration
	MaxAVSync    time.Duration

	// Composite
	Score float64
}

// Compute derives the metric set from the quantized Observation.
// audioOnly/videoOnly come from the recording's intended track set;
// they shape Score (videos in audio-only outputs aren't penalized).
//
// Within a bucket, every event's fracOffset is measured as
// `event.PTS - (bucket*1s + Offset)`. For a perfectly stable beep this
// is 0 by construction (Offset is inferred from event PTS medians);
// flashes' fracOffsets relative to the same reference reveal the
// AV-sync gap and its drift across the recording.
func Compute(obs *Observation, audioOnly, videoOnly bool) Stats {
	var s Stats
	s.AudioOnly = audioOnly
	s.VideoOnly = videoOnly

	if obs == nil {
		s.Score = math.Round(score(s)*10) / 10
		return s
	}

	for _, sec := range obs.Buckets {
		for _, beeps := range sec.Beeps {
			s.BeepCount += len(beeps)
		}
		for _, flashes := range sec.Flashes {
			s.FlashCount += len(flashes)
		}
	}

	stableBucket, timeToStable := obs.TimeToStabilize()
	s.Locked = stableBucket >= 0
	s.TimeToStable = timeToStable
	if !s.Locked {
		s.Score = math.Round(score(s)*10) / 10
		return s
	}

	var audioFracs, videoFracs []time.Duration
	var stableDiffs []time.Duration
	for i, sec := range obs.Buckets {
		if i < stableBucket {
			continue
		}
		if len(sec.Beeps) == 0 && len(sec.Flashes) == 0 {
			continue
		}
		var aFracs, vFracs []time.Duration
		for _, beeps := range sec.Beeps {
			for _, b := range beeps {
				aFracs = append(aFracs, b.PTS-sec.Center)
			}
		}
		for _, flashes := range sec.Flashes {
			for _, f := range flashes {
				vFracs = append(vFracs, f.PTS-sec.Center)
			}
		}

		audioFracs = append(audioFracs, aFracs...)
		videoFracs = append(videoFracs, vFracs...)
		if len(aFracs) > 0 && len(vFracs) > 0 {
			stableDiffs = append(stableDiffs, medianDuration(vFracs)-medianDuration(aFracs))
		}
	}

	s.AudioJitter = stdDevDuration(audioFracs)
	s.VideoJitter = stdDevDuration(videoFracs)
	if len(audioFracs) > 0 && len(videoFracs) > 0 {
		s.StableAVSync = medianDuration(videoFracs) - medianDuration(audioFracs)
	}
	if len(stableDiffs) > 0 {
		s.AVSyncStdDev = stdDevDuration(stableDiffs)
		for _, d := range stableDiffs {
			if a := absDuration(d); a > s.MaxAVSync {
				s.MaxAVSync = a
			}
		}
	}

	s.Score = math.Round(score(s)*10) / 10
	return s
}

// Score is the exported entry point — it rounds to 1 decimal so tests
// and reports get the same value as Compute writes into Stats.Score.
func Score(s Stats) float64 {
	return math.Round(score(s)*10) / 10
}

// score collapses Stats into a 0–100 score. Weights and thresholds
// come from the design spec; see the spec for rationale.
//
// Returns 0 if the recording produced no usable cadence data (no
// stable region in either track that was expected) — otherwise a
// broken recording with zero events would silently score 100 and
// disappear from the "worst score" column of the aggregate table.
func score(s Stats) float64 {
	if !s.Locked {
		// Observation never produced a stable region. Score 0 so the row
		// doesn't disappear from the aggregate's "worst score" column.
		// (TimeToStable == 0 alone is NOT a broken signal: it can mean
		// the recording locked from the very first event, e.g., H264
		// sample at 25fps with first flash on frame 0.)
		return 0
	}
	if !s.AudioOnly && !s.VideoOnly && (s.BeepCount == 0 || s.FlashCount == 0) {
		// Both tracks expected but one produced no events; the AV-sync
		// penalties contribute 0 (they only run when both sides have
		// stable events), so the formula would score a half-broken
		// recording implausibly well.
		return 0
	}

	out := 100.0
	out -= 20.0 * normalize(durMs(s.AVSyncStdDev), 10, 50)
	out -= 20.0 * normalize(durMs(absDuration(s.StableAVSync)), 50, 300)
	out -= 20.0 * normalize(durMs(s.AudioJitter), 5, 50)
	out -= 20.0 * normalize(durMs(s.VideoJitter), 5, 50)
	out -= 10.0 * normalize(durMs(s.MaxAVSync), 100, 500)
	out -= 10.0 * normalize(durSec(s.TimeToStable), 1, 10)
	if out < 0 {
		out = 0
	}
	return out
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

// DeriveSource buckets a LiveKit requestType into "web" or "sdk".
// Returns "" for unknown values.
func DeriveSource(requestType string) string {
	switch requestType {
	case types.RequestTypeRoomComposite,
		types.RequestTypeWeb,
		types.RequestTypeTemplate:
		return "web"
	case types.RequestTypeParticipant,
		types.RequestTypeTrackComposite,
		types.RequestTypeTrack,
		types.RequestTypeMedia:
		return "sdk"
	}
	return ""
}

// --- helpers ---

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

func durMs(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func durSec(d time.Duration) float64 {
	return float64(d) / float64(time.Second)
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

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
