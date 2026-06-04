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
	"fmt"
	"image"
	"math"

	"github.com/livekit/media-samples/avsync"
)

const (
	// gridGap is --grid-gap: 0.5rem = 8px; applied both as cell gap and outer padding.
	gridGap = 8
	// regionInset insets sampling regions to avoid edge compression artifacts.
	// Top is intentionally not inset: the flash bar lives at the tile top and is
	// only ~12px tall on carousel thumbnails.
	regionInset = 20
	// minCarouselTileHeight mirrors CarouselLayout.tsx MIN_HEIGHT.
	minCarouselTileHeight = 130
)

// maxCarouselTiles mirrors CarouselLayout.tsx vertical sizing.
func maxCarouselTiles(carouselW, innerH int) int {
	tileSpan := carouselW * 6 / 10
	if tileSpan < minCarouselTileHeight {
		tileSpan = minCarouselTileHeight
	}
	n := (innerH + tileSpan/2) / tileSpan
	if n < 1 {
		n = 1
	}
	return n
}

// insetRect shrinks r by margin on the sides and bottom only. The top edge is
// preserved so the flash strip remains within the region.
func insetRect(r image.Rectangle, margin int) image.Rectangle {
	return image.Rectangle{
		Min: image.Pt(r.Min.X+margin, r.Min.Y),
		Max: image.Pt(r.Max.X-margin, r.Max.Y-margin),
	}
}

// SpeakerLayoutRegions returns the expected sampling regions for the speaker
// (active-speaker) layout template.
//
// The template renders as a CSS grid with two columns – 1fr carousel on the
// left and 5fr stage on the right – separated by --grid-gap (8px), and with
// 8px padding all around.  Carousel thumbnails are stacked vertically, each
// with a 16:10 aspect ratio, also separated by 8px gaps.
//
// The first region is the stage (the dominant speaker).  The remaining regions
// are the carousel thumbnails, one per participant, ordered top to bottom.
//
// numParticipants is the total number of participants in the room (including
// the dominant speaker).
func SpeakerLayoutRegions(width, height, numParticipants int) []avsync.Region {
	totalFr := 6
	innerW := width - 2*gridGap
	innerH := height - 2*gridGap
	availableForCols := innerW - gridGap
	carouselW := availableForCols / totalFr
	stageW := availableForCols - carouselW
	stageX := gridGap + carouselW + gridGap

	stage := image.Rectangle{
		Min: image.Pt(stageX, gridGap),
		Max: image.Pt(stageX+stageW, gridGap+innerH),
	}

	regions := []avsync.Region{
		{
			Name: "stage",
			Rect: insetRect(stage, regionInset),
		},
	}

	maxTiles := maxCarouselTiles(carouselW, innerH)
	thumbCount := numParticipants - 1
	if thumbCount > maxTiles {
		thumbCount = maxTiles
	}
	thumbW := carouselW
	thumbH := (innerH - gridGap*(maxTiles-1)) / maxTiles
	for i := 0; i < thumbCount; i++ {
		thumbY := gridGap + i*(thumbH+gridGap)
		thumb := image.Rectangle{
			Min: image.Pt(gridGap, thumbY),
			Max: image.Pt(gridGap+thumbW, thumbY+thumbH),
		}
		regions = append(regions, avsync.Region{
			Name: fmt.Sprintf("thumb%d", i),
			Rect: insetRect(thumb, regionInset),
		})
	}

	return regions
}

// GridLayoutRegions returns the expected sampling regions for the grid layout
// template.
//
// The column count follows the LiveKit components algorithm:
//
//	1 participant  → 1 column
//	2–4            → 2 columns (≥560px) or 1 column
//	5–9            → 3 columns (≥700px)
//	10–16          → 4 columns (≥960px)
//	17+            → 5 columns (≥1100px)
//
// Rows are determined by ceil(numParticipants / cols).  All cells are equal in
// size, separated by --grid-gap (8px), with 8px padding around the grid.
func GridLayoutRegions(width, height, numParticipants int) []avsync.Region {
	cols := gridColumns(width, numParticipants)
	rows := int(math.Ceil(float64(numParticipants) / float64(cols)))

	innerW := width - 2*gridGap
	innerH := height - 2*gridGap
	cellW := (innerW - (cols-1)*gridGap) / cols
	cellH := (innerH - (rows-1)*gridGap) / rows

	regions := make([]avsync.Region, 0, numParticipants)
	for i := 0; i < numParticipants; i++ {
		col := i % cols
		row := i / cols
		x := gridGap + col*(cellW+gridGap)
		y := gridGap + row*(cellH+gridGap)
		cell := image.Rectangle{
			Min: image.Pt(x, y),
			Max: image.Pt(x+cellW, y+cellH),
		}
		regions = append(regions, avsync.Region{
			Name: fmt.Sprintf("cell%d", i),
			Rect: insetRect(cell, regionInset),
		})
	}
	return regions
}

// SingleSpeakerLayoutRegions returns the expected sampling region for the
// single-speaker layout: essentially full-frame with an inset margin.
func SingleSpeakerLayoutRegions(width, height int) []avsync.Region {
	full := image.Rectangle{
		Min: image.Pt(0, 0),
		Max: image.Pt(width, height),
	}
	return []avsync.Region{
		{
			Name: "stage",
			Rect: insetRect(full, regionInset),
		},
	}
}

// gridColumns returns the number of grid columns for the given viewport width
// and participant count, matching the LiveKit components default algorithm.
func gridColumns(width, numParticipants int) int {
	switch {
	case numParticipants <= 1:
		return 1
	case numParticipants <= 4:
		if width >= 560 {
			return 2
		}
		return 1
	case numParticipants <= 9:
		if width >= 700 {
			return 3
		}
		return 2
	case numParticipants <= 16:
		if width >= 960 {
			return 4
		}
		return 3
	default:
		if width >= 1100 {
			return 5
		}
		return 4
	}
}
