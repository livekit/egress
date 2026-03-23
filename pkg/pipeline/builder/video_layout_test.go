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

	"github.com/stretchr/testify/require"
)

func TestSelectGrid(t *testing.T) {
	tests := []struct {
		count   int
		cols    int
		rows    int
	}{
		{1, 1, 1},
		{2, 2, 1},
		{3, 2, 2},
		{4, 2, 2},
		{5, 3, 3},
		{9, 3, 3},
		{10, 4, 4},
		{16, 4, 4},
		{17, 5, 5},
		{25, 5, 5},
		{26, 5, 5}, // clamped to max
	}

	for _, tt := range tests {
		g := selectGrid(tt.count)
		require.Equal(t, tt.cols, g.Columns, "cols for count=%d", tt.count)
		require.Equal(t, tt.rows, g.Rows, "rows for count=%d", tt.count)
	}
}

func TestComputeGridPositions(t *testing.T) {
	lm := NewLayoutManager(1920, 1080, LayoutGrid)

	t.Run("single participant", func(t *testing.T) {
		pos := lm.ComputePositions(1)
		require.Len(t, pos, 1)
		require.Equal(t, TilePosition{0, 0, 1920, 1080}, pos[0])
	})

	t.Run("two participants", func(t *testing.T) {
		pos := lm.ComputePositions(2)
		require.Len(t, pos, 2)
		require.Equal(t, TilePosition{0, 0, 960, 1080}, pos[0])
		require.Equal(t, TilePosition{960, 0, 960, 1080}, pos[1])
	})

	t.Run("four participants 2x2", func(t *testing.T) {
		pos := lm.ComputePositions(4)
		require.Len(t, pos, 4)
		require.Equal(t, TilePosition{0, 0, 960, 540}, pos[0])
		require.Equal(t, TilePosition{960, 0, 960, 540}, pos[1])
		require.Equal(t, TilePosition{0, 540, 960, 540}, pos[2])
		require.Equal(t, TilePosition{960, 540, 960, 540}, pos[3])
	})

	t.Run("three participants in 2x2 grid", func(t *testing.T) {
		pos := lm.ComputePositions(3)
		require.Len(t, pos, 3)
		// 2x2 grid, third participant in first cell of second row
		require.Equal(t, TilePosition{0, 0, 960, 540}, pos[0])
		require.Equal(t, TilePosition{960, 0, 960, 540}, pos[1])
		require.Equal(t, TilePosition{0, 540, 960, 540}, pos[2])
	})

	t.Run("zero participants", func(t *testing.T) {
		pos := lm.ComputePositions(0)
		require.Nil(t, pos)
	})
}
