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

// LayoutType selects the compositing layout algorithm.
type LayoutType int

const (
	LayoutGrid LayoutType = iota
)

// TilePosition describes where a participant's video is placed in the output frame.
type TilePosition struct {
	X, Y, W, H int
}

type gridSize struct {
	Columns, Rows int
}

// Standard grid progression matching @livekit/components-core grid-layouts.ts
var gridLayouts = []gridSize{
	{1, 1}, // 1 participant
	{2, 1}, // 2 participants
	{2, 2}, // 3-4 participants
	{3, 3}, // 5-9 participants
	{4, 4}, // 10-16 participants
	{5, 5}, // 17-25 participants
}

// LayoutManager computes tile positions for video compositing.
type LayoutManager struct {
	outputW, outputH int
	layoutType       LayoutType
}

// NewLayoutManager creates a layout manager for the given output dimensions and layout type.
func NewLayoutManager(outputW, outputH int, layoutType LayoutType) *LayoutManager {
	return &LayoutManager{
		outputW:    outputW,
		outputH:    outputH,
		layoutType: layoutType,
	}
}

// ComputePositions returns tile positions for the given participant count.
func (l *LayoutManager) ComputePositions(participantCount int) []TilePosition {
	return l.computeGridPositions(participantCount)
}

func selectGrid(participantCount int) gridSize {
	for _, g := range gridLayouts {
		if g.Columns*g.Rows >= participantCount {
			return g
		}
	}
	return gridLayouts[len(gridLayouts)-1]
}

func (l *LayoutManager) computeGridPositions(participantCount int) []TilePosition {
	if participantCount <= 0 {
		return nil
	}

	grid := selectGrid(participantCount)
	tileW := l.outputW / grid.Columns
	tileH := l.outputH / grid.Rows

	positions := make([]TilePosition, participantCount)
	for i := 0; i < participantCount; i++ {
		col := i % grid.Columns
		row := i / grid.Columns
		positions[i] = TilePosition{
			X: col * tileW,
			Y: row * tileH,
			W: tileW,
			H: tileH,
		}
	}
	return positions
}
