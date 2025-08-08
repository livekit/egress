// Copyright 2023 LiveKit, Inc.
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

package builder

import (
	"bytes"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/go-gst/go-gst/gst"
)

const (
	srcPadName = "src"
)

type gstTimeInfo struct {
	pts      gst.ClockTime
	duration gst.ClockTime
}

type audioMixerStats struct {
	mixer     *gst.Element
	ptsRanges map[string][]gstTimeInfo
	sync.Mutex
}

func (ams *audioMixerStats) sinkProbe(padName string, info *gst.PadProbeInfo) gst.PadProbeReturn {
	buffer := info.GetBuffer()
	if buffer == nil {
		return gst.PadProbeOK
	}
	buffPTS := buffer.PresentationTimestamp()
	bufDuration := buffer.Duration()
	fmt.Printf("***[%d]<%s>Buffer pts: %s, duration: %s", getGID(), padName, buffPTS.String(), bufDuration.String())
	if buffer.Duration() == gst.ClockTimeNone {
		// ignore buffers without duration
		return gst.PadProbeOK
	}

	ams.Lock()
	defer ams.Unlock()

	ranges := ams.ptsRanges[padName]
	var prevPts gst.ClockTime
	var prevDuration gst.ClockTime

	if len(ranges) > 0 {
		prevPts = ranges[len(ranges)-1].pts
		prevDuration = ranges[len(ranges)-1].duration
	}

	if prevPts > 0 && (buffPTS != prevPts+prevDuration) {
		fmt.Printf("***<%s>Discontinuity discovered at: %s\n", padName, buffPTS.String())
		fmt.Printf("***<%s>Existing ranges: %s\n", padName, gstRangesToString(ranges))
		fmt.Printf("***<%s>Diff: %d\n", padName, buffPTS-(prevPts+prevDuration))
	}
	ams.mergeRanges(padName, gstTimeInfo{
		pts:      buffPTS,
		duration: bufDuration,
	})
	return gst.PadProbeOK
}

func (ams *audioMixerStats) srcProbe(info *gst.PadProbeInfo) gst.PadProbeReturn {
	buffer := info.GetBuffer()
	if buffer == nil {
		return gst.PadProbeOK
	}
	if buffer.Duration() == gst.ClockTimeNone {
		// ignore buffers without duration
		return gst.PadProbeOK
	}

	buffPTS := info.GetBuffer().PresentationTimestamp()
	buffDuration := info.GetBuffer().Duration()

	fmt.Printf("***[%d]<%s>Buffer pts %s, duration: %s", getGID(), srcPadName, buffPTS.String(), buffDuration.String())

	// lock is only needed to guard top level map access, consider using sync.Map
	ams.Lock()
	defer ams.Unlock()

	gstTimeinfo := gstTimeInfo{
		pts:      buffPTS,
		duration: buffer.Duration(),
	}

	if !ams.isRangeCoveredBySinks(gstTimeinfo) {
		fmt.Printf("***<%s>Source range not covered by sinks: %s", srcPadName, buffPTS.String())
	}

	ams.mergeRanges(srcPadName, gstTimeinfo)
	return gst.PadProbeOK
}

func (amd *audioMixerStats) mergeRanges(padName string, newRange gstTimeInfo) {
	ranges := amd.ptsRanges[padName]
	start := newRange.pts
	end := start + newRange.duration

	merged := []gstTimeInfo{}

	for _, r := range ranges {
		rStart := r.pts
		rEnd := rStart + r.duration

		// Check for overlap or consecutive (touching) ranges
		if end < rStart || start > rEnd {
			// No overlap
			merged = append(merged, r)
			continue
		}

		// Merge overlapping/consecutive ranges
		start = min(start, rStart)
		end = max(end, rEnd)
	}

	// Insert the new (merged) range
	merged = append(merged, gstTimeInfo{
		pts:      start,
		duration: end - start,
	})

	// Update the pad's range list (re-sort to help future merges if needed)
	amd.ptsRanges[padName] = sortAndMerge(merged)
}

func (ams *audioMixerStats) isRangeCoveredBySinks(srcRange gstTimeInfo) bool {
	srcStart := srcRange.pts
	srcEnd := srcStart + srcRange.duration

	for sinkPad, ranges := range ams.ptsRanges {
		if !strings.HasPrefix(sinkPad, "sink") {
			continue // skip non-sink pads
		}

		if len(ranges) == 0 {
			return false // no data on this sink pad
		}

		covered := false
		for _, r := range ranges {
			rStart := r.pts
			rEnd := rStart + r.duration

			if srcStart >= rStart && srcEnd <= rEnd {
				covered = true
				break
			}
		}

		if !covered {
			return false // this sink pad does not fully cover the src range
		}
	}

	return true // all sink pads cover the src range
}

func gstRangesToString(ranges []gstTimeInfo) string {
	if len(ranges) == 0 {
		return "[]"
	}

	var str string
	for _, r := range ranges {
		str += fmt.Sprintf("{pts: %s, duration: %s}, ", r.pts.String(), r.duration.String())
	}
	return "[" + str[:len(str)-2] + "]"
}

func sortAndMerge(ranges []gstTimeInfo) []gstTimeInfo {
	if len(ranges) == 0 {
		return nil
	}

	// Sort by pts
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].pts < ranges[j].pts
	})

	merged := []gstTimeInfo{ranges[0]}
	for i := 1; i < len(ranges); i++ {
		last := merged[len(merged)-1]
		curr := ranges[i]

		lastStart := last.pts
		lastEnd := lastStart + last.duration
		currStart := curr.pts
		currEnd := currStart + curr.duration

		if currStart <= lastEnd { // overlap or consecutive
			merged[len(merged)-1] = gstTimeInfo{
				pts:      min(lastStart, currStart),
				duration: max(lastEnd, currEnd) - min(lastStart, currStart),
			}
		} else {
			merged = append(merged, curr)
		}
	}
	return merged
}

func getGID() uint64 {
	b := make([]byte, 64)
	b = b[:runtime.Stack(b, false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	i := bytes.IndexByte(b, ' ')
	if i < 0 {
		panic(fmt.Sprintf("No space found in %q", b))
	}
	b = b[:i]
	n, err := strconv.ParseUint(string(b), 10, 64)
	if err != nil {
		panic(fmt.Sprintf("Failed to parse goroutine ID out of %q: %v", b, err))
	}
	return n
}
