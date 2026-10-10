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
// recording PTS. Offset is the global median fractional offset;
// beepRefs/flashRefs are per-participant medians used by hasOutlier
// (so systematic cross-track/cross-participant offsets don't block
// stabilization — those are measured separately as av-sync).
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

	// Per-participant medians for hasOutlier. Assumes a participant's
	// fracOffset doesn't drift across the 0/1s boundary within a recording.
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

// 50ms tolerates per-codec detection latency variance without letting
// actually-misaligned events count as stable.
const stableFracTolerance = 50 * time.Millisecond

// TimeToStabilize finds the first run of 3 consecutive non-empty
// non-outlier buckets and returns (bucketIndex, earliestPTS), or
// (-1, 0) if no such run exists. Empty buckets are skipped entirely:
// they don't count toward the run, but don't reset it either (so
// intentional silence in multi-participant recordings is tolerated).
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

// hasOutlier checks each event against its own (participant, track-type)
// reference fracOffset — systematic cross-track/cross-participant offsets
// shouldn't disqualify a bucket, only within-stream jitter should.
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

// wrappedFracDiff returns a - b on a 1-second circle, in (-500ms, 500ms].
// e.g. wrappedFracDiff(950ms, 50ms) = -100ms, not +900ms.
func wrappedFracDiff(a, b time.Duration) time.Duration {
	d := a - b
	if d > 500*time.Millisecond {
		d -= time.Second
	} else if d <= -500*time.Millisecond {
		d += time.Second
	}
	return d
}

// earliestPTS returns the smallest PTS across all events in the bucket;
// ok is false for empty buckets.
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

// IntegerLag returns the candidate lag (0..maxIntLag) that best aligns
// actual buckets to expected plan slots. Smallest lag wins ties.
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
				// silent-matches-silent: prevents lag from drifting past
				// long forbidden zones just because more buckets align further out.
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
