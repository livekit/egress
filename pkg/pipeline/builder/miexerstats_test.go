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
	"testing"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/assert"
)

func mkTime(n int64) gst.ClockTime {
	return gst.ClockTime(n)
}

func TestSortAndMerge(t *testing.T) {
	tests := []struct {
		name   string
		input  []gstTimeInfo
		expect []gstTimeInfo
	}{
		{
			name: "non-overlapping",
			input: []gstTimeInfo{
				{pts: mkTime(0), duration: mkTime(10)},
				{pts: mkTime(20), duration: mkTime(10)},
			},
			expect: []gstTimeInfo{
				{pts: mkTime(0), duration: mkTime(10)},
				{pts: mkTime(20), duration: mkTime(10)},
			},
		},
		{
			name: "consecutive",
			input: []gstTimeInfo{
				{pts: mkTime(0), duration: mkTime(10)},
				{pts: mkTime(10), duration: mkTime(10)},
			},
			expect: []gstTimeInfo{
				{pts: mkTime(0), duration: mkTime(20)},
			},
		},
		{
			name: "overlapping",
			input: []gstTimeInfo{
				{pts: mkTime(0), duration: mkTime(15)},
				{pts: mkTime(10), duration: mkTime(10)},
			},
			expect: []gstTimeInfo{
				{pts: mkTime(0), duration: mkTime(20)},
			},
		},
		{
			name: "unordered",
			input: []gstTimeInfo{
				{pts: mkTime(30), duration: mkTime(10)},
				{pts: mkTime(0), duration: mkTime(10)},
				{pts: mkTime(10), duration: mkTime(10)},
			},
			expect: []gstTimeInfo{
				{pts: mkTime(0), duration: mkTime(20)},
				{pts: mkTime(30), duration: mkTime(10)},
			},
		},
		{
			name: "merge two distant ranges with one bridging",
			input: []gstTimeInfo{
				{pts: mkTime(0), duration: mkTime(10)},
				{pts: mkTime(30), duration: mkTime(10)},
				{pts: mkTime(10), duration: mkTime(20)},
			},
			expect: []gstTimeInfo{
				{pts: mkTime(0), duration: mkTime(40)},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sortAndMerge(tc.input)
			assert.Equal(t, tc.expect, got)
		})
	}
}

func TestIsRangeCoveredBySinks(t *testing.T) {
	stats := &audioMixerStats{
		ptsRanges: map[string][]gstTimeInfo{
			"sink_0": {
				{pts: mkTime(0), duration: mkTime(20)},
			},
			"sink_1": {
				{pts: mkTime(0), duration: mkTime(20)},
			},
		},
	}

	t.Run("fully covered", func(t *testing.T) {
		src := gstTimeInfo{pts: mkTime(5), duration: mkTime(10)}
		stats.mergeRanges("sink_0", src)
		assert.True(t, stats.isRangeCoveredBySinks(src))
	})

	t.Run("not covered by one sink", func(t *testing.T) {
		stats.ptsRanges["sink_1"] = []gstTimeInfo{{pts: mkTime(10), duration: mkTime(10)}}
		src := gstTimeInfo{pts: mkTime(5), duration: mkTime(10)}
		assert.False(t, stats.isRangeCoveredBySinks(src))
	})

	t.Run("no sink ranges", func(t *testing.T) {
		stats.ptsRanges["sink_0"] = []gstTimeInfo{}
		stats.ptsRanges["sink_1"] = []gstTimeInfo{}
		src := gstTimeInfo{pts: mkTime(0), duration: mkTime(5)}
		assert.False(t, stats.isRangeCoveredBySinks(src))
	})
}
