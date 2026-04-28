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

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTagsCompatible(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{
			name: "both empty",
			a:    nil,
			b:    nil,
			want: true,
		},
		{
			name: "audio + audio",
			a:    []string{"audio"},
			b:    []string{"audio"},
			want: true,
		},
		{
			name: "audio + video conflict",
			a:    []string{"audio"},
			b:    []string{"video"},
			want: false,
		},
		{
			name: "audio with audio+video compat",
			a:    []string{"audio"},
			b:    []string{"audio+video"},
			want: true,
		},
		{
			name: "video with audio+video compat",
			a:    []string{"video"},
			b:    []string{"audio+video"},
			want: true,
		},
		{
			name: "track + composite conflict",
			a:    []string{"track"},
			b:    []string{"composite"},
			want: false,
		},
		{
			name: "composite + composite",
			a:    []string{"composite"},
			b:    []string{"composite"},
			want: true,
		},
		{
			name: "mixed multi-tag: audio,track vs video,composite",
			a:    []string{"audio", "track"},
			b:    []string{"video", "composite"},
			want: false,
		},
		{
			name: "mixed multi-tag: audio+video,composite vs video,composite",
			a:    []string{"audio+video", "composite"},
			b:    []string{"video", "composite"},
			want: true,
		},
		{
			name: "audio+video vs audio+video",
			a:    []string{"audio+video"},
			b:    []string{"audio+video"},
			want: true,
		},
		{
			name: "unrelated tags",
			a:    []string{"foo"},
			b:    []string{"bar"},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tagsCompatible(tt.a, tt.b)
			assert.Equal(t, tt.want, got)
			// Symmetry: order should not matter.
			got2 := tagsCompatible(tt.b, tt.a)
			assert.Equal(t, tt.want, got2, "tagsCompatible should be symmetric")
		})
	}
}

func TestCoverSimple(t *testing.T) {
	// 2 dimensions, 2 values each, no tags.
	dims := [][]DimValue{
		{
			{Dimension: "color", Name: "red"},
			{Dimension: "color", Name: "blue"},
		},
		{
			{Dimension: "size", Name: "small"},
			{Dimension: "size", Name: "large"},
		},
	}

	tests := Cover(dims)

	// With 2x2 and no tag conflicts, 2 tests should cover all 4 values.
	require.Len(t, tests, 2, "expected 2 tests to cover 2x2 dimensions")

	// Verify every value is covered.
	covered := make(map[string]bool)
	for _, test := range tests {
		for _, v := range test.Values {
			covered[v.Dimension+"/"+v.Name] = true
		}
	}
	assert.True(t, covered["color/red"])
	assert.True(t, covered["color/blue"])
	assert.True(t, covered["size/small"])
	assert.True(t, covered["size/large"])
}

func TestCoverWithIncompatibility(t *testing.T) {
	// 2 dimensions where some values conflict via tags.
	dims := [][]DimValue{
		{
			{Dimension: "request", Name: "TrackComposite", Tags: []string{"composite"}},
			{Dimension: "request", Name: "Track", Tags: []string{"track"}},
		},
		{
			{Dimension: "output", Name: "HLS", Tags: []string{"composite"}},
			{Dimension: "output", Name: "OGG", Tags: []string{"track"}},
		},
	}

	tests := Cover(dims)

	// There must be enough tests to cover all 4 values.
	covered := make(map[string]bool)
	for _, test := range tests {
		for _, v := range test.Values {
			covered[v.Dimension+"/"+v.Name] = true
		}

		// Verify no test pairs incompatible values.
		for i := 0; i < len(test.Values); i++ {
			for j := i + 1; j < len(test.Values); j++ {
				assert.True(t,
					tagsCompatible(test.Values[i].Tags, test.Values[j].Tags),
					"test %q has incompatible values: %s and %s",
					test.Name, test.Values[i].Name, test.Values[j].Name,
				)
			}
		}
	}

	assert.True(t, covered["request/TrackComposite"])
	assert.True(t, covered["request/Track"])
	assert.True(t, covered["output/HLS"])
	assert.True(t, covered["output/OGG"])
}

func TestCoverDeterministic(t *testing.T) {
	dims := [][]DimValue{
		{
			{Dimension: "codec", Name: "H264", Tags: []string{"video"}},
			{Dimension: "codec", Name: "VP8", Tags: []string{"video"}},
			{Dimension: "codec", Name: "Opus", Tags: []string{"audio"}},
		},
		{
			{Dimension: "output", Name: "MP4", Tags: []string{"audio+video"}},
			{Dimension: "output", Name: "OGG", Tags: []string{"audio"}},
			{Dimension: "output", Name: "WebM", Tags: []string{"audio+video"}},
		},
	}

	run1 := Cover(dims)
	run2 := Cover(dims)

	require.Equal(t, len(run1), len(run2), "number of tests should be identical across runs")
	for i := range run1 {
		assert.Equal(t, run1[i].Name, run2[i].Name, "test %d name mismatch", i)
		require.Equal(t, len(run1[i].Values), len(run2[i].Values), "test %d value count mismatch", i)
		for j := range run1[i].Values {
			assert.Equal(t, run1[i].Values[j], run2[i].Values[j], "test %d value %d mismatch", i, j)
		}
	}
}
