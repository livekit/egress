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
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func (r *Runner) testMultiSync(t *testing.T) {
	if !r.should(runMultiSync) {
		return
	}

	t.Run("MultiSync", func(t *testing.T) {
		for _, test := range []*testCase{
			// Note: publishOptions left empty so r.run() does NOT publish from the
			// primary participant. MultiPublisher handles all publishing in custom funcs.
			{
				name:        "MultiParticipantSync/3p-grid",
				requestType: types.RequestTypeRoomComposite,
				fileOptions: &fileOptions{
					filename:   "multi_sync_3p_grid_{time}.mp4",
					outputType: types.OutputTypeMP4,
				},
				custom: func(t *testing.T, tc *testCase) {
					r.testMultiParticipantSync(t, tc, 3, "grid", 5*time.Second)
				},
			},
			{
				name:        "MultiParticipantSync/3p-speaker",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					layout: "speaker",
				},
				fileOptions: &fileOptions{
					filename:   "multi_sync_3p_speaker_{time}.mp4",
					outputType: types.OutputTypeMP4,
				},
				custom: func(t *testing.T, tc *testCase) {
					r.testMultiParticipantSync(t, tc, 3, "speaker", 5*time.Second)
				},
			},
			{
				name:        "MultiParticipantScreenShare",
				requestType: types.RequestTypeRoomComposite,
				fileOptions: &fileOptions{
					filename:   "multi_screen_share_{time}.mp4",
					outputType: types.OutputTypeMP4,
				},
				custom: func(t *testing.T, tc *testCase) {
					r.testMultiParticipantScreenShare(t, tc)
				},
			},
			{
				name:        "MultiParticipantAudioRouting",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioMixing: livekit.AudioMixing_DUAL_CHANNEL_AGENT,
				},
				fileOptions: &fileOptions{
					filename:   "multi_audio_routing_{time}.mp4",
					outputType: types.OutputTypeMP4,
				},
				custom: func(t *testing.T, tc *testCase) {
					r.testMultiParticipantAudioRouting(t, tc)
				},
			},
		} {
			if !r.run(t, test, test.custom) {
				return
			}
		}
	})
}

func (r *Runner) testMultiParticipantSync(t *testing.T, test *testCase, numParticipants int, layout string, turnDuration time.Duration) {
	mp := NewMultiPublisher(t, r, numParticipants, true, true)
	mp.StartRotation(turnDuration)

	// Give tracks time to be subscribed
	time.Sleep(3 * time.Second)

	req := r.buildRequest(test)
	egressID := r.startEgress(t, req)

	// Record for at least one full rotation
	recordDuration := turnDuration*time.Duration(numParticipants) + 5*time.Second
	time.Sleep(recordDuration)

	res := r.stopEgress(t, egressID)

	// Get output file path
	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	fileRes := res.GetFile() //nolint:staticcheck
	if fileRes == nil {
		require.Len(t, res.FileResults, 1)
		fileRes = res.FileResults[0]
	}
	outputFile := fileRes.Filename
	_ = p

	// Build participant specs with expected tile positions
	specs := buildParticipantSpecs(mp, layout, int(p.Width), int(p.Height))

	// Run sync analysis
	tolerance := 300 * time.Millisecond
	results := AnalyzeSync(t, outputFile, specs, turnDuration, tolerance, r.FilePrefix)

	for _, result := range results {
		t.Logf("participant=%s intra=%v inter=%v audio=%v video=%v",
			result.Participant, result.IntraOffset, result.InterOffset,
			result.AudioPresent, result.VideoPresent)
	}
}

func (r *Runner) testMultiParticipantScreenShare(t *testing.T, test *testCase) {
	// 2 camera participants
	mp := NewMultiPublisher(t, r, 2, true, true)

	// Add a screen share track from participant 0
	ssFile, ssFd := videoFile(0)
	ssTrack, err := lksdk.NewLocalFileTrack(ssFile,
		lksdk.ReaderTrackWithOnWriteComplete(func() {}),
		lksdk.ReaderTrackWithFrameDuration(ssFd),
	)
	require.NoError(t, err)
	ssPub, err := mp.Participants()[0].LocalParticipant.PublishTrack(ssTrack, &lksdk.TrackPublicationOptions{
		Name:   "screen-share",
		Source: livekit.TrackSource_SCREEN_SHARE,
	})
	require.NoError(t, err)
	_ = ssPub

	time.Sleep(3 * time.Second)

	req := r.buildRequest(test)
	egressID := r.startEgress(t, req)

	time.Sleep(15 * time.Second)
	res := r.stopEgress(t, egressID)

	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	fileRes := res.GetFile() //nolint:staticcheck
	if fileRes == nil {
		require.Len(t, res.FileResults, 1)
		fileRes = res.FileResults[0]
	}
	outputFile := fileRes.Filename

	// With a screen share, grid should auto-switch to speaker layout.
	// Screen share should be in the focus position (75% width).
	focusW := int(p.Width) * 3 / 4
	focusFlashes, err := extractRegionFlashes(outputFile, 0, 0, focusW, int(p.Height), r.FilePrefix)
	require.NoError(t, err)
	require.NotEmpty(t, focusFlashes, "screen share should produce flashes in focus region")
}

func (r *Runner) testMultiParticipantAudioRouting(t *testing.T, test *testCase) {
	// 2 users + 1 agent
	mp := NewMultiPublisher(t, r, 2, true, true)

	// Connect an agent participant separately
	agentRoom, agentPub := r.connectAgent(t, 2) // index 2 = 1320Hz
	t.Cleanup(agentRoom.Disconnect)
	_ = agentPub

	// Start rotation among user participants only
	mp.StartRotation(5 * time.Second)

	time.Sleep(3 * time.Second)

	req := r.buildRequest(test)
	egressID := r.startEgress(t, req)

	time.Sleep(20 * time.Second)
	res := r.stopEgress(t, egressID)

	fileRes := res.GetFile() //nolint:staticcheck
	if fileRes == nil {
		require.Len(t, res.FileResults, 1)
		fileRes = res.FileResults[0]
	}
	outputFile := fileRes.Filename

	// With DUAL_CHANNEL_AGENT, agent audio is routed to a separate channel.
	// Verify user frequencies (440Hz, 880Hz) are present in the mix.
	for _, freq := range []float64{440, 880} {
		events, err := extractFrequencyAudio(outputFile, freq, r.FilePrefix)
		require.NoError(t, err)
		require.NotEmpty(t, events, "user audio (%.0fHz) should be present in mix", freq)
	}

	// Verify agent's frequency (1320Hz) is present (it's in the output, just on a separate channel)
	agentEvents, err := extractFrequencyAudio(outputFile, 1320, r.FilePrefix)
	require.NoError(t, err)
	require.NotEmpty(t, agentEvents, "agent audio (1320Hz) should be present in dual-channel output")
}

// connectAgent connects an agent participant and publishes audio at the given participant index's frequency.
func (r *Runner) connectAgent(t *testing.T, participantIdx int) (*lksdk.Room, *lksdk.LocalTrackPublication) {
	t.Helper()
	room, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "mp-agent",
		ParticipantIdentity: fmt.Sprintf("mp-agent-%d", rand.Intn(1000)),
		ParticipantKind:     lksdk.ParticipantAgent,
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)

	agentFile, agentFd := audioFile(participantIdx)
	opts := []lksdk.ReaderSampleProviderOption{
		lksdk.ReaderTrackWithOnWriteComplete(func() {}),
	}
	if agentFd != 0 {
		opts = append(opts, lksdk.ReaderTrackWithFrameDuration(agentFd))
	}
	track, err := lksdk.NewLocalFileTrack(agentFile, opts...)
	require.NoError(t, err)

	pub, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: agentFile})
	require.NoError(t, err)

	return room, pub
}

// buildParticipantSpecs creates ParticipantSpec entries for sync analysis
// based on the layout type and canvas dimensions.
func buildParticipantSpecs(mp *MultiPublisher, layout string, canvasW, canvasH int) []ParticipantSpec {
	n := mp.N()
	media := mp.ParticipantMedia()
	specs := make([]ParticipantSpec, n)

	switch layout {
	case "speaker":
		// Focus: 75% width, full height. Carousel: 25% width, stacked.
		focusW := canvasW * 3 / 4
		carouselW := canvasW - focusW
		maxCarousel := 3
		if n-1 < maxCarousel {
			maxCarousel = n - 1
		}
		carouselH := canvasH / maxCarousel

		specs[0] = ParticipantSpec{
			Identity:  mp.Participants()[0].LocalParticipant.Identity(),
			Frequency: media[0].Frequency,
			TileX:     0,
			TileY:     0,
			TileW:     focusW,
			TileH:     canvasH,
		}
		for i := 1; i < n && i <= maxCarousel; i++ {
			specs[i] = ParticipantSpec{
				Identity:  mp.Participants()[i].LocalParticipant.Identity(),
				Frequency: media[i].Frequency,
				TileX:     focusW,
				TileY:     (i - 1) * carouselH,
				TileW:     carouselW,
				TileH:     carouselH,
			}
		}

	default: // "grid"
		cols, rows := selectGridForTest(n)
		tileW := canvasW / cols
		tileH := canvasH / rows

		for i := 0; i < n; i++ {
			col := i % cols
			row := i / cols
			specs[i] = ParticipantSpec{
				Identity:  mp.Participants()[i].LocalParticipant.Identity(),
				Frequency: media[i].Frequency,
				TileX:     col * tileW,
				TileY:     row * tileH,
				TileW:     tileW,
				TileH:     tileH,
			}
		}
	}

	return specs
}

// selectGridForTest mirrors the LayoutManager's grid selection for test verification.
func selectGridForTest(n int) (cols, rows int) {
	grids := [][2]int{{1, 1}, {2, 1}, {2, 2}, {3, 3}, {4, 4}, {5, 5}}
	for _, g := range grids {
		if g[0]*g[1] >= n {
			return g[0], g[1]
		}
	}
	return 5, 5
}
