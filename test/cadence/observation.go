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
	"slices"
	"time"

	"github.com/livekit/media-samples/avsync"
)

// Observation bins detected beeps and flashes by integer second of
// recording PTS, preserving the avsync.Beep / avsync.Flash payloads.
//
// Offset is the global median fractional offset across all events; it
// drives bucket assignment and the Bucket.Center used for av-sync diff
// math. beepRefs/flashRefs hold per-participant median fractional
// offsets, used by hasOutlier so that systematic cross-track or
// cross-participant offsets do not block stabilization — they're
// measured separately via the av-sync diff.
type Observation struct {
	Buckets []*Bucket
	Offset  time.Duration

	beepRefs  map[string]time.Duration
	flashRefs map[string]time.Duration
}

type Bucket struct {
	Beeps   map[string][]avsync.Beep
	Flashes map[string][]avsync.Flash
	Center  time.Duration
}

// Quantize bins beeps/flashes from the analyzer result into per-second
// Buckets centered on the median fractional PTS offset.
func Quantize(result *avsync.Result) *Observation {
	obs := &Observation{}

	if result == nil || len(result.Beeps)+len(result.Flashes) == 0 {
		return obs
	}

	maxPTS := time.Duration(0)
	fracs := make([]time.Duration, 0, len(result.Beeps)+len(result.Flashes))
	for _, b := range result.Beeps {
		fracs = append(fracs, b.PTS%time.Second)
		if b.PTS > maxPTS {
			maxPTS = b.PTS
		}
	}
	for _, f := range result.Flashes {
		fracs = append(fracs, f.PTS%time.Second)
		if f.PTS > maxPTS {
			maxPTS = f.PTS
		}
	}
	slices.Sort(fracs)

	// center buckets around the median fractional pts
	obs.Offset = fracs[len(fracs)/2]
	getBucket := func(t time.Duration) int64 {
		adj := t + 500*time.Millisecond
		if obs.Offset > 500*time.Millisecond {
			adj += time.Second
		}
		return int64((adj - obs.Offset) / time.Second)
	}

	// create/fill buckets
	numBuckets := getBucket(maxPTS) + 1
	for i := range numBuckets {
		b := &Bucket{
			Beeps:   make(map[string][]avsync.Beep),
			Flashes: make(map[string][]avsync.Flash),
			Center:  time.Duration(i)*time.Second + obs.Offset,
		}
		if obs.Offset > 500*time.Millisecond {
			b.Center -= time.Second
		}

		obs.Buckets = append(obs.Buckets, b)
	}
	beepFracs := make(map[string][]time.Duration)
	flashFracs := make(map[string][]time.Duration)
	for _, b := range result.Beeps {
		s := obs.Buckets[getBucket(b.PTS)]
		s.Beeps[b.Participant] = append(s.Beeps[b.Participant], b)
		beepFracs[b.Participant] = append(beepFracs[b.Participant], b.PTS%time.Second)
	}
	for _, f := range result.Flashes {
		s := obs.Buckets[getBucket(f.PTS)]
		s.Flashes[f.Participant] = append(s.Flashes[f.Participant], f)
		flashFracs[f.Participant] = append(flashFracs[f.Participant], f.PTS%time.Second)
	}

	// Per-participant fracOffset medians, used by hasOutlier. Assumes a
	// single participant's recording PTS doesn't drift across the 0/1s
	// boundary within a recording (it doesn't in practice; jitter is
	// ms-scale, not ~1s).
	obs.beepRefs = make(map[string]time.Duration, len(beepFracs))
	for p, fs := range beepFracs {
		obs.beepRefs[p] = medianDuration(fs)
	}
	obs.flashRefs = make(map[string]time.Duration, len(flashFracs))
	for p, fs := range flashFracs {
		obs.flashRefs[p] = medianDuration(fs)
	}

	return obs
}

// stableFracTolerance is the maximum allowed deviation from the median
// fracOffset for an event to be in the "stable region." Beep/flash
// detection latency varies by a few ms across codecs; 50ms tolerates
// that without letting actually-misaligned events count as stable.
const stableFracTolerance = 50 * time.Millisecond

// TimeToStabilize finds the first run of 3 consecutive non-empty
// non-outlier buckets and returns the first one's index and earliest
// event PTS. Empty buckets are skipped entirely — they don't reset
// the run (so intentional silence in multi-participant recordings
// doesn't break stability) but they also don't count toward it.
// Returns (-1, 0) if no 3-bucket stable run exists.
func (obs *Observation) TimeToStabilize() (int, time.Duration) {
	if obs == nil || len(obs.Buckets) == 0 {
		return -1, 0
	}

	firstStableBucket := -1
	firstPTS := time.Duration(0)
	valid := 0
	for i, b := range obs.Buckets {
		if obs.hasOutlier(b) {
			firstStableBucket = -1
			valid = 0
			continue
		}
		pts, ok := earliestPTS(b)
		if !ok {
			continue
		}
		if firstStableBucket < 0 {
			firstStableBucket = i
			firstPTS = pts
		}
		valid++
		if valid >= 3 {
			return firstStableBucket, firstPTS
		}
	}

	return -1, 0
}

// hasOutlier reports whether any event in this bucket deviates from its
// own (participant, track-type) reference fracOffset by more than
// stableFracTolerance. Using per-participant per-track-type references
// means systematic offsets between audio and video, or between
// different participants' streams, don't disqualify stable buckets —
// only within-stream jitter does. Cross-stream offsets are measured
// separately by Compute via the av-sync diff.
func (obs *Observation) hasOutlier(b *Bucket) bool {
	for participant, beeps := range b.Beeps {
		ref := obs.beepRefs[participant]
		for _, ev := range beeps {
			if absDuration(wrappedFracDiff(ev.PTS%time.Second, ref)) > stableFracTolerance {
				return true
			}
		}
	}
	for participant, flashes := range b.Flashes {
		ref := obs.flashRefs[participant]
		for _, ev := range flashes {
			if absDuration(wrappedFracDiff(ev.PTS%time.Second, ref)) > stableFracTolerance {
				return true
			}
		}
	}
	return false
}

// wrappedFracDiff returns a - b, treating both as points on a
// 1-second circle and returning the smallest signed difference in
// (-500ms, 500ms]. For inputs in [0, 1s), a value like
// wrappedFracDiff(950ms, 50ms) returns -100ms rather than +900ms.
func wrappedFracDiff(a, b time.Duration) time.Duration {
	d := a - b
	if d > 500*time.Millisecond {
		d -= time.Second
	} else if d <= -500*time.Millisecond {
		d += time.Second
	}
	return d
}

// earliestPTS returns the smallest PTS across all events in this bucket.
// ok is false if the bucket is empty.
func earliestPTS(b *Bucket) (time.Duration, bool) {
	var first time.Duration
	found := false
	for _, beeps := range b.Beeps {
		for _, ev := range beeps {
			if !found || ev.PTS < first {
				first = ev.PTS
				found = true
			}
		}
	}
	for _, flashes := range b.Flashes {
		for _, ev := range flashes {
			if !found || ev.PTS < first {
				first = ev.PTS
				found = true
			}
		}
	}
	return first, found
}

// IntegerLag scores each candidate lag (0..maxIntLag) and returns the
// best. Score is the count of buckets where actual and expected agree:
//   - actual bucket has a beep whose participant is in expected[i+lag]
//   - both actual bucket and expected[i+lag] are empty (silent matches
//     silent — keeps the lag from drifting past long forbidden zones
//     just because the remaining content fits more buckets at a higher
//     lag)
//
// Smallest lag wins ties.
func IntegerLag(expected [][]string, actual []*Bucket) int64 {
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
			if len(actual[i].Beeps) == 0 {
				if len(expected[ei]) == 0 {
					score++
				}
				continue
			}
			for _, beeps := range actual[i].Beeps {
				for _, b := range beeps {
					for _, name := range expected[ei] {
						if b.Participant == name {
							score++
							break
						}
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
