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
		X: 0, Y: 0,
		W: 1920, H: 1080,
		ZOrder: 1, Alpha: 1.0,
	}, pads[0])
}

func TestGridLayout_TwoParticipants(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	lm.AddTrack("track1", "alice", TrackSourceCamera)
	pads := lm.AddTrack("track2", "bob", TrackSourceCamera)

	require.Len(t, pads, 2)
	// 2 participants on landscape → 2x1 grid
	require.Equal(t, 960, pads[0].W)
	require.Equal(t, 1080, pads[0].H)
	require.Equal(t, 0, pads[0].X)

	require.Equal(t, 960, pads[1].W)
	require.Equal(t, 1080, pads[1].H)
	require.Equal(t, 960, pads[1].X)
}

func TestGridLayout_FourParticipants(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	lm.AddTrack("t2", "bob", TrackSourceCamera)
	lm.AddTrack("t3", "carol", TrackSourceCamera)
	pads := lm.AddTrack("t4", "dave", TrackSourceCamera)

	require.Len(t, pads, 4)
	// 4 participants → 2x2 grid
	require.Equal(t, 960, pads[0].W)
	require.Equal(t, 540, pads[0].H)
	require.Equal(t, 0, pads[0].X)
	require.Equal(t, 0, pads[0].Y)

	require.Equal(t, 960, pads[1].X)
	require.Equal(t, 0, pads[1].Y)

	require.Equal(t, 0, pads[2].X)
	require.Equal(t, 540, pads[2].Y)

	require.Equal(t, 960, pads[3].X)
	require.Equal(t, 540, pads[3].Y)
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
	// 9 participants → 3x3 grid
	require.Equal(t, 640, pads[0].W)
	require.Equal(t, 360, pads[0].H)
}

func TestGridLayout_RemoveTrack(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	lm.AddTrack("t2", "bob", TrackSourceCamera)
	lm.AddTrack("t3", "carol", TrackSourceCamera)

	pads := lm.RemoveTrack("t2")
	require.Len(t, pads, 2)
	// Back to 2 participants → 2x1
	require.Equal(t, 960, pads[0].W)
	require.Equal(t, 1080, pads[0].H)
}

func TestGridLayout_Portrait(t *testing.T) {
	lm := NewLayoutManager("grid", 720, 1280)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	pads := lm.AddTrack("t2", "bob", TrackSourceCamera)

	require.Len(t, pads, 2)
	// 2 participants on portrait → 1x2 (stacked)
	require.Equal(t, 720, pads[0].W)
	require.Equal(t, 640, pads[0].H)
	require.Equal(t, 0, pads[0].Y)

	require.Equal(t, 720, pads[1].W)
	require.Equal(t, 640, pads[1].H)
	require.Equal(t, 640, pads[1].Y)
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
	require.Equal(t, 0, pads[0].X)
	require.Equal(t, 1440, pads[0].W)
	require.Equal(t, 1080, pads[0].H)

	require.Equal(t, 1440, pads[1].X)
	require.Equal(t, 480, pads[1].W)
	require.Equal(t, 1080, pads[1].H)
}

func TestSpeakerLayout_FourTracks(t *testing.T) {
	lm := NewLayoutManager("speaker", 1920, 1080)
	lm.AddTrack("t1", "alice", TrackSourceCamera)
	lm.AddTrack("t2", "bob", TrackSourceCamera)
	lm.AddTrack("t3", "carol", TrackSourceCamera)
	pads := lm.AddTrack("t4", "dave", TrackSourceCamera)

	require.Len(t, pads, 4)
	require.Equal(t, 1440, pads[0].W)
	require.Equal(t, 1080, pads[0].H)
	require.Equal(t, 1440, pads[1].X)
	require.Equal(t, 480, pads[1].W)
	require.Equal(t, 360, pads[1].H)
	require.Equal(t, 0, pads[1].Y)

	require.Equal(t, 360, pads[2].Y)
	require.Equal(t, 720, pads[3].Y)
}

func TestSpeakerLayout_MoreThanFourTracks(t *testing.T) {
	lm := NewLayoutManager("speaker", 1920, 1080)
	for i := 0; i < 5; i++ {
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
	require.Equal(t, 4, visible) // 1 focus + 3 carousel
	require.Equal(t, 1, hidden)
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
	require.Equal(t, 1440, pads[0].W) // focus width
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
	require.Equal(t, 1440, pads[0].W)
}

func TestScreenShare_RevertsToGrid(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	lm.AddTrack("cam1", "alice", TrackSourceCamera)
	lm.AddTrack("cam2", "bob", TrackSourceCamera)
	lm.AddTrack("ss1", "bob", TrackSourceScreenShare)

	pads := lm.RemoveTrack("ss1")

	require.Len(t, pads, 2)
	require.Equal(t, 960, pads[0].W) // grid 2x1 tile width
}

func TestScreenShare_SpeakerLayoutStays(t *testing.T) {
	lm := NewLayoutManager("speaker", 1920, 1080)
	lm.AddTrack("cam1", "alice", TrackSourceCamera)
	lm.AddTrack("cam2", "bob", TrackSourceCamera)

	pads := lm.AddTrack("ss1", "bob", TrackSourceScreenShare)

	require.Equal(t, "ss1", pads[0].TrackID)
	require.Equal(t, 1440, pads[0].W)
}

func TestScreenShare_MultipleScreenShares(t *testing.T) {
	lm := NewLayoutManager("grid", 1920, 1080)
	lm.AddTrack("cam1", "alice", TrackSourceCamera)
	lm.AddTrack("cam2", "bob", TrackSourceCamera)
	lm.AddTrack("ss1", "bob", TrackSourceScreenShare)

	pads := lm.AddTrack("ss2", "alice", TrackSourceScreenShare)

	require.Equal(t, "ss1", pads[0].TrackID)
	require.Equal(t, 1440, pads[0].W)
}
