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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpeakerLayoutRegions(t *testing.T) {
	const (
		w            = 1920
		h            = 1080
		participants = 3
	)

	regions := SpeakerLayoutRegions(w, h, participants)

	// Expect 1 stage + numParticipants thumbnails.
	require.Len(t, regions, 1+participants)

	stage := regions[0]
	assert.Equal(t, "stage", stage.Name)

	// Stage must be on the right side, well over 1500px wide.
	stageW := stage.Rect.Max.X - stage.Rect.Min.X
	assert.Greater(t, stageW, 1500, "stage width should exceed 1500px")

	// Stage x origin must be beyond the carousel (> 300px from left).
	assert.Greater(t, stage.Rect.Min.X, 300, "stage should start well to the right")

	// All thumbnails must be on the left side (< 350px wide, x < 350).
	for i := 1; i <= participants; i++ {
		thumb := regions[i]
		assert.Equal(t, "thumb"+string(rune('0'+i-1)), thumb.Name)
		thumbW := thumb.Rect.Max.X - thumb.Rect.Min.X
		assert.Less(t, thumbW, 350, "thumbnail %d width should be < 350px", i)
		assert.Less(t, thumb.Rect.Max.X, 350, "thumbnail %d should be in carousel column", i)
	}
}

func TestGridLayoutRegions(t *testing.T) {
	const (
		w            = 1920
		h            = 1080
		participants = 3
	)

	regions := GridLayoutRegions(w, h, participants)
	require.Len(t, regions, participants)

	// With 3 participants at 1920px we expect 2 columns (2–4 rule, width ≥ 560).
	// Verify all cells have equal dimensions.
	refW := regions[0].Rect.Max.X - regions[0].Rect.Min.X
	refH := regions[0].Rect.Max.Y - regions[0].Rect.Min.Y
	for i, r := range regions {
		cellW := r.Rect.Max.X - r.Rect.Min.X
		cellH := r.Rect.Max.Y - r.Rect.Min.Y
		assert.Equal(t, refW, cellW, "cell %d width mismatch", i)
		assert.Equal(t, refH, cellH, "cell %d height mismatch", i)
	}

	// Cells must not overlap.
	for i := 0; i < len(regions); i++ {
		for j := i + 1; j < len(regions); j++ {
			ri := regions[i].Rect
			rj := regions[j].Rect
			overlap := ri.Intersect(rj)
			assert.True(t, overlap.Empty(), "cells %d and %d must not overlap", i, j)
		}
	}
}

func TestSingleSpeakerLayoutRegions(t *testing.T) {
	const (
		w = 1920
		h = 1080
	)

	regions := SingleSpeakerLayoutRegions(w, h)
	require.Len(t, regions, 1)

	r := regions[0]
	assert.Equal(t, "stage", r.Name)

	// Region should cover nearly the full frame (only regionInset margin trimmed).
	regionW := r.Rect.Max.X - r.Rect.Min.X
	regionH := r.Rect.Max.Y - r.Rect.Min.Y
	assert.Equal(t, w-2*regionInset, regionW, "single speaker region width")
	assert.Equal(t, h-2*regionInset, regionH, "single speaker region height")

	// Anchored at (regionInset, regionInset).
	assert.Equal(t, regionInset, r.Rect.Min.X)
	assert.Equal(t, regionInset, r.Rect.Min.Y)
}
