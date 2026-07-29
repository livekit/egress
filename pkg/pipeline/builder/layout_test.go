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

package builder

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGridLayout_SingleParticipant(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	pads := lm.AddTrack("track1", "alice", TrackSourceCamera)

	require.Len(t, pads, 1)
	require.Equal(t, PadLayout{
		TrackID: "track1",
		X:       0, Y: 0,
		W: 1920, H: 1080,
		ZOrder: 1, Alpha: 1.0,
	}, pads[0])
}

func TestGridLayout_TwoParticipants(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	pads := lm.AddTrack("t2", "bob", TrackSourceCamera)

	require.Len(t, pads, 2)
	// innerW=1904, innerH=1064, 2 cells, 1 row → cellW = (1904-8)/2 = 948
	require.Equal(t, 8, pads[0].X)
	require.Equal(t, 8, pads[0].Y)
	require.Equal(t, 948, pads[0].W)
	require.Equal(t, 1064, pads[0].H)

	require.Equal(t, 964, pads[1].X)
	require.Equal(t, 8, pads[1].Y)
	require.Equal(t, 948, pads[1].W)
	require.Equal(t, 1064, pads[1].H)
}

func TestGridLayout_ThreeParticipants(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	lm.AddTrack("t2", "bob", TrackSourceCamera)
	pads := lm.AddTrack("t3", "carol", TrackSourceCamera)

	require.Len(t, pads, 3)
	// 3 participants → 2 cols × 2 rows, last cell empty (matches Chrome)
	// innerW=1904, innerH=1064 → cellW=948, cellH=(1064-8)/2=528
	require.Equal(t, 948, pads[0].W)
	require.Equal(t, 528, pads[0].H)
	require.Equal(t, 8, pads[0].X)
	require.Equal(t, 8, pads[0].Y)

	require.Equal(t, 964, pads[1].X)
	require.Equal(t, 8, pads[1].Y)

	require.Equal(t, 8, pads[2].X)
	require.Equal(t, 544, pads[2].Y)
}

func TestGridLayout_FourParticipants(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	lm.AddTrack("t2", "bob", TrackSourceCamera)
	lm.AddTrack("t3", "carol", TrackSourceCamera)
	pads := lm.AddTrack("t4", "dave", TrackSourceCamera)

	require.Len(t, pads, 4)
	require.Equal(t, 948, pads[0].W)
	require.Equal(t, 528, pads[0].H)
	require.Equal(t, 8, pads[0].X)
	require.Equal(t, 8, pads[0].Y)

	require.Equal(t, 964, pads[1].X)
	require.Equal(t, 8, pads[1].Y)

	require.Equal(t, 8, pads[2].X)
	require.Equal(t, 544, pads[2].Y)

	require.Equal(t, 964, pads[3].X)
	require.Equal(t, 544, pads[3].Y)
}

func TestGridLayout_NineParticipants(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	for i := 0; i < 8; i++ {
		lm.AddTrack(
			"t"+string(rune('1'+i)),
			string(rune('a'+i)),
			TrackSourceCamera,
		)
	}
	pads := lm.AddTrack("t9", "i", TrackSourceCamera)

	require.Len(t, pads, 9)
	// 9 participants @ 1920w → 3 cols × 3 rows.
	// innerW=1904, innerH=1064 → cellW=(1904-16)/3=629, cellH=(1064-16)/3=349
	require.Equal(t, 629, pads[0].W)
	require.Equal(t, 349, pads[0].H)
}

func TestGridLayout_RemoveTrack(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	lm.AddTrack("t2", "bob", TrackSourceCamera)
	lm.AddTrack("t3", "carol", TrackSourceCamera)

	pads := lm.RemoveTrack("t2")
	require.Len(t, pads, 2)
	// Back to 2 participants → 2 cols × 1 row
	require.Equal(t, 948, pads[0].W)
	require.Equal(t, 1064, pads[0].H)
}

func TestGridLayout_NarrowWidth(t *testing.T) {
	// width < 560 → 1 col for 2-4 participants (stacked)
	lm := NewLayoutManager("grid", 400, 1280)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	pads := lm.AddTrack("t2", "bob", TrackSourceCamera)

	require.Len(t, pads, 2)
	// innerW=384, innerH=1264 → cellW=384, cellH=(1264-8)/2=628
	require.Equal(t, 384, pads[0].W)
	require.Equal(t, 628, pads[0].H)
	require.Equal(t, 8, pads[0].Y)

	require.Equal(t, 384, pads[1].W)
	require.Equal(t, 628, pads[1].H)
	require.Equal(t, 644, pads[1].Y)
}

func TestSpeakerLayout_SingleTrack(t *testing.T) {
	lm := NewLayoutManager("speaker", 1920, 1080)
	pads := lm.AddTrack("t1", "alice", TrackSourceCamera)

	require.Len(t, pads, 1)
	require.Equal(t, 0, pads[0].X)
	require.Equal(t, 0, pads[0].Y)
	require.Equal(t, 1920, pads[0].W)
	require.Equal(t, 1080, pads[0].H)
}

func TestSpeakerLayout_TwoTracks(t *testing.T) {
	lm := NewLayoutManager("speaker", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	pads := lm.AddTrack("t2", "bob", TrackSourceCamera)

	require.Len(t, pads, 2)
	// innerW=1904, innerH=1064.
	// carouselW = (1904-8)/6 = 316, stageW = 1896-316 = 1580, stageX = 8+316+8 = 332.
	require.Equal(t, 332, pads[0].X)
	require.Equal(t, 8, pads[0].Y)
	require.Equal(t, 1580, pads[0].W)
	require.Equal(t, 1064, pads[0].H)

	// maxTiles = round(1064 / max(316*0.6, 130)) = round(1064/189) = 6
	// thumbH = (1064 - 8*5)/6 = 170
	require.Equal(t, 8, pads[1].X)
	require.Equal(t, 8, pads[1].Y)
	require.Equal(t, 316, pads[1].W)
	require.Equal(t, 170, pads[1].H)
}

func TestSpeakerLayout_FourTracks(t *testing.T) {
	lm := NewLayoutManager("speaker", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	lm.AddTrack("t2", "bob", TrackSourceCamera)
	lm.AddTrack("t3", "carol", TrackSourceCamera)
	pads := lm.AddTrack("t4", "dave", TrackSourceCamera)

	require.Len(t, pads, 4)
	require.Equal(t, 332, pads[0].X)
	require.Equal(t, 1580, pads[0].W)
	require.Equal(t, 1064, pads[0].H)

	require.Equal(t, 8, pads[1].X)
	require.Equal(t, 316, pads[1].W)
	require.Equal(t, 170, pads[1].H)
	require.Equal(t, 8, pads[1].Y)

	// Thumbs stack with 8px gap: Y[i] = 8 + (i-1)*(170+8)
	require.Equal(t, 186, pads[2].Y)
	require.Equal(t, 364, pads[3].Y)
}

func TestSpeakerLayout_MoreThanFitTracks(t *testing.T) {
	// At 1920×1080 the carousel fits 6 tiles. With 9 participants → 1 stage +
	// 6 visible carousel + 2 hidden.
	lm := NewLayoutManager("speaker", 1920, 1080)
	for i := 0; i < 9; i++ {
		lm.AddTrack(
			"t"+string(rune('1'+i)),
			string(rune('a'+i)),
			TrackSourceCamera,
		)
	}
	pads := lm.recalculate()

	visible := 0
	hidden := 0
	for _, p := range pads {
		if p.Alpha > 0 {
			visible++
		} else {
			hidden++
		}
	}
	require.Equal(t, 7, visible) // 1 stage + 6 carousel
	require.Equal(t, 2, hidden)
}

func TestSingleSpeakerLayout(t *testing.T) {
	lm := NewLayoutManager("single-speaker", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	pads := lm.AddTrack("t2", "bob", TrackSourceCamera)

	require.Len(t, pads, 2)
	require.Equal(t, 1.0, pads[0].Alpha)
	require.Equal(t, 1920, pads[0].W)
	require.Equal(t, 1080, pads[0].H)

	require.Equal(t, 0.0, pads[1].Alpha)
}

func TestUpdateSpeakers_SortsBySpeaking(t *testing.T) {
	lm := NewLayoutManager("speaker", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	lm.AddTrack("t2", "bob", TrackSourceCamera)
	lm.AddTrack("t3", "carol", TrackSourceCamera)

	pads := lm.UpdateSpeakers([]SpeakerInfo{
		{Identity: "bob", AudioLevel: 0.9, IsSpeaking: true},
	})

	require.NotNil(t, pads)
	require.Equal(t, "t2", pads[0].TrackID)
	require.Equal(t, 1580, pads[0].W) // stage width
}

func TestUpdateSpeakers_AudioLevelTiebreak(t *testing.T) {
	lm := NewLayoutManager("speaker", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	lm.AddTrack("t2", "bob", TrackSourceCamera)
	lm.AddTrack("t3", "carol", TrackSourceCamera)

	pads := lm.UpdateSpeakers([]SpeakerInfo{
		{Identity: "bob", AudioLevel: 0.5, IsSpeaking: true},
		{Identity: "carol", AudioLevel: 0.9, IsSpeaking: true},
	})

	require.NotNil(t, pads)
	require.Equal(t, "t3", pads[0].TrackID)
}

func TestUpdateSpeakers_Stabilization(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	lm.AddTrack("t2", "bob", TrackSourceCamera)
	lm.AddTrack("t3", "carol", TrackSourceCamera)
	lm.AddTrack("t4", "dave", TrackSourceCamera)

	initialPads := lm.recalculate()
	require.Equal(t, "t1", initialPads[0].TrackID)

	// Dave speaks — all 4 fit on one page (2x2), so no swap needed
	pads := lm.UpdateSpeakers([]SpeakerInfo{
		{Identity: "dave", AudioLevel: 0.8, IsSpeaking: true},
	})
	require.Len(t, pads, 4)
}

func TestUpdateSpeakers_LastSpokeAtTiebreak(t *testing.T) {
	lm := NewLayoutManager("speaker", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	lm.AddTrack("t2", "bob", TrackSourceCamera)
	lm.AddTrack("t3", "carol", TrackSourceCamera)

	// Bob speaks
	lm.UpdateSpeakers([]SpeakerInfo{
		{Identity: "bob", AudioLevel: 0.8, IsSpeaking: true},
	})
	// Bob stops, carol speaks
	pads := lm.UpdateSpeakers([]SpeakerInfo{
		{Identity: "carol", AudioLevel: 0.7, IsSpeaking: true},
	})

	require.NotNil(t, pads)
	require.Equal(t, "t3", pads[0].TrackID)
	require.Equal(t, "t2", pads[1].TrackID)
}

func TestScreenShare_GridSwitchesToSpeaker(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	lm.AddTrack("cam1", "alice", TrackSourceCamera)
	lm.AddTrack("cam2", "bob", TrackSourceCamera)

	pads := lm.AddTrack("ss1", "bob", TrackSourceScreenShare)

	require.Equal(t, "ss1", pads[0].TrackID)
	require.Equal(t, 1580, pads[0].W) // speaker stage width
}

func TestScreenShare_RevertsToGrid(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	lm.AddTrack("cam1", "alice", TrackSourceCamera)
	lm.AddTrack("cam2", "bob", TrackSourceCamera)
	lm.AddTrack("ss1", "bob", TrackSourceScreenShare)

	pads := lm.RemoveTrack("ss1")

	require.Len(t, pads, 2)
	require.Equal(t, 948, pads[0].W) // grid 2x1 cell width
}

func TestScreenShare_SpeakerLayoutStays(t *testing.T) {
	lm := NewLayoutManager("speaker", 1920, 1080)
	lm.AddTrack("cam1", "alice", TrackSourceCamera)
	lm.AddTrack("cam2", "bob", TrackSourceCamera)

	pads := lm.AddTrack("ss1", "bob", TrackSourceScreenShare)

	require.Equal(t, "ss1", pads[0].TrackID)
	require.Equal(t, 1580, pads[0].W)
}

func TestScreenShare_MultipleScreenShares(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	lm.AddTrack("cam1", "alice", TrackSourceCamera)
	lm.AddTrack("cam2", "bob", TrackSourceCamera)
	lm.AddTrack("ss1", "bob", TrackSourceScreenShare)

	pads := lm.AddTrack("ss2", "alice", TrackSourceScreenShare)

	require.Equal(t, "ss1", pads[0].TrackID)
	require.Equal(t, 1580, pads[0].W)
}
