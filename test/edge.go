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

//go:build integration

package test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func (r *Runner) testEdgeCases(t *testing.T) {
	if !r.should(runEdge) {
		return
	}

	t.Run("EdgeCases", func(t *testing.T) {
		for _, test := range []*testCase{

			// Agents with room composite audio only

			{
				name:        "Agents",
				requestType: types.RequestTypeRoomComposite,
				fileOptions: &fileOptions{
					filename: "agents_{time}",
				},
				custom: r.testAgents,
			},

			// RoomComposite audio mixing

			{
				name:        "AudioMixing",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioOnly:   true,
					audioMixing: livekit.AudioMixing_DUAL_CHANNEL_AGENT,
				},
				fileOptions: &fileOptions{
					filename: "audio_mixing_{time}",
				},
				custom: r.testAudioMixing,
			},

			// ParticipantComposite where the participant never publishes

			{
				name:        "ParticipantNoPublish",
				requestType: types.RequestTypeParticipant,
				fileOptions: &fileOptions{
					filename: "participant_no_publish_{time}.mp4",
				},
				custom: r.testParticipantNoPublish,
			},

			// Test that the egress continues if a user leaves

			{
				name:        "RoomCompositeStaysOpen",
				requestType: types.RequestTypeRoomComposite,
				fileOptions: &fileOptions{
					filename: "room_composite_stays_open_{time}.mp4",
				},
				custom: r.testRoomCompositeStaysOpen,
			},

			// RTMP output with no valid urls

			{
				name:        "RtmpFailure",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeH264,
				},
				streamOptions: &streamOptions{
					streamUrls: []string{badRtmpUrl1},
					outputType: types.OutputTypeRTMP,
				},
				custom: r.testRtmpFailure,
			},

			// SRT output with no valid urls

			{
				name:        "SrtFailure",
				requestType: types.RequestTypeWeb,
				streamOptions: &streamOptions{
					streamUrls: []string{badSrtUrl1},
					outputType: types.OutputTypeSRT,
				},
				custom: r.testSrtFailure,
			},

			// Track composite with data loss due to a disconnection

			{
				name:        "TrackDisconnection",
				requestType: types.RequestTypeTrackComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
				},
				fileOptions: &fileOptions{
					filename: "track_disconnection_{time}.mp4",
					fileType: livekit.EncodedFileType_MP4,
				},
				custom: r.testTrackDisconnection,
			},

			// Stream output with no urls

			{
				name:        "EmptyStreamBin",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeVP8,
				},
				streamOptions: &streamOptions{
					streamUrls: []string{rtmpUrl4, badRtmpUrl1},
					outputType: types.OutputTypeRTMP,
				},
				segmentOptions: &segmentOptions{
					prefix:   "empty_stream_{time}",
					playlist: "empty_stream_{time}",
				},
				custom: r.testEmptyStreamBin,
			},

			// File storage limit reached

			{
				name:        "FileStorageLimit",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeVP8,
				},
				fileOptions: &fileOptions{
					filename: "storage_limit_{time}.mp4",
					fileType: livekit.EncodedFileType_MP4,
				},
				custom: r.testStorageLimit,
			},
		} {
			if !r.run(t, test, test.custom) {
				return
			}
		}
	})
}

func (r *Runner) testAgents(t *testing.T, test *testCase) {
	_, err := os.Stat("/agents/.env")
	if err != nil {
		t.Skip("skipping agents test; missing env file")
	}

	r.launchAgents(t)
	time.Sleep(time.Second * 5)
	r.runFileTest(t, test)
}

func (r *Runner) testAudioMixing(t *testing.T, test *testCase) {
	p1, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "egress-sample-1",
		ParticipantIdentity: fmt.Sprintf("sample-1-%d", rand.Intn(100)),
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	t.Cleanup(p1.Disconnect)
	r.publish(t, p1.LocalParticipant, types.MimeTypeOpus, make(chan struct{}))

	agent, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "egress-sample",
		ParticipantIdentity: fmt.Sprintf("agent-%d", rand.Intn(100)),
		ParticipantKind:     lksdk.ParticipantAgent,
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	t.Cleanup(agent.Disconnect)
	r.publish(t, agent.LocalParticipant, types.MimeTypeOpus, make(chan struct{}))

	p2, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "egress-sample",
		ParticipantIdentity: fmt.Sprintf("sample-2-%d", rand.Intn(100)),
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	t.Cleanup(p2.Disconnect)
	r.publish(t, p2.LocalParticipant, types.MimeTypeOpus, make(chan struct{}))

	r.runFileTest(t, test)
}

func (r *Runner) testParticipantNoPublish(t *testing.T, test *testCase) {
	identity := r.room.LocalParticipant.Identity()

	req := r.build(test)

	info := r.sendRequest(t, req)
	time.Sleep(time.Second * 15)
	r.room.Disconnect()
	time.Sleep(time.Second * 30)
	info = r.getUpdate(t, info.EgressId)
	require.Equal(t, livekit.EgressStatus_EGRESS_ABORTED.String(), info.Status.String())

	// reconnect the publisher to the room
	room, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "egress-sample",
		ParticipantIdentity: identity,
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	r.room = room
}

func (r *Runner) testRoomCompositeStaysOpen(t *testing.T, test *testCase) {
	req := r.build(test)

	info := r.sendRequest(t, req)
	time.Sleep(time.Second * 10)
	identity := r.room.LocalParticipant.Identity()
	r.room.Disconnect()
	time.Sleep(time.Second * 10)

	// reconnect the publisher to the room
	room, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "egress-sample",
		ParticipantIdentity: identity,
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	r.room = room

	r.publishSample(t, types.MimeTypeOpus, 0, 0, false)
	r.publishSample(t, types.MimeTypeVP8, 0, 0, false)

	time.Sleep(time.Second * 10)

	r.checkUpdate(t, info.EgressId, livekit.EgressStatus_EGRESS_ACTIVE)
	r.stopEgress(t, info.EgressId)
}

func (r *Runner) testStorageLimit(t *testing.T, test *testCase) {
	origLimit := r.ServiceConfig.SessionLimits.FileOutputMaxSize
	r.ServiceConfig.SessionLimits.FileOutputMaxSize = 300000 // ~300KB to trigger quickly
	t.Cleanup(func() {
		r.ServiceConfig.SessionLimits.FileOutputMaxSize = origLimit
	})

	req := r.build(test)
	info := r.sendRequest(t, req)
	egressID := info.EgressId

	deadline := time.After(45 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for storage limit")
		default:
		}

		update := r.getUpdate(t, egressID)
		switch update.Status {
		case livekit.EgressStatus_EGRESS_ACTIVE:
			continue
		case livekit.EgressStatus_EGRESS_LIMIT_REACHED:
			file := update.GetFile() //nolint:staticcheck // keep deprecated field for older clients
			if file == nil && len(update.FileResults) > 0 {
				file = update.FileResults[0]
			}
			require.NotNil(t, file)
			require.Contains(t, update.Details, livekit.EndReasonLimitReached)
			require.NotEmpty(t, update.Error)
			return
		case livekit.EgressStatus_EGRESS_FAILED:
			t.Fatalf("egress failed: %s", update.Error)
		default:
			continue
		}
	}
}

func (r *Runner) testRtmpFailure(t *testing.T, test *testCase) {
	req := r.build(test)

	info, err := r.StartEgress(context.Background(), req)
	require.NoError(t, err)
	require.Empty(t, info.Error)
	require.NotEmpty(t, info.EgressId)
	require.Equal(t, r.RoomName, info.RoomName)
	require.Equal(t, livekit.EgressStatus_EGRESS_STARTING, info.Status)

	// check updates
	time.Sleep(time.Second * 5)
	info = r.getUpdate(t, info.EgressId)
	streamFailed := false
	for info.Status == livekit.EgressStatus_EGRESS_ACTIVE {
		if !streamFailed && info.StreamResults[0].Status == livekit.StreamInfo_FAILED {
			streamFailed = true
		}
		if streamFailed {
			// make sure this never reverts in subsequent updates
			require.Equal(t, livekit.StreamInfo_FAILED, info.StreamResults[0].Status)
		}
		info = r.getUpdate(t, info.EgressId)
	}

	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, info.Status)
	require.NotEmpty(t, info.Error)
	require.Equal(t, livekit.StreamInfo_FAILED, info.StreamResults[0].Status)
	require.NotEmpty(t, info.StreamResults[0].Error)
}

func (r *Runner) testSrtFailure(t *testing.T, test *testCase) {
	req := r.build(test)

	info, err := r.StartEgress(context.Background(), req)
	require.NoError(t, err)
	require.Empty(t, info.Error)
	require.NotEmpty(t, info.EgressId)
	require.Equal(t, livekit.EgressStatus_EGRESS_STARTING, info.Status)

	// check update
	time.Sleep(time.Second * 5)
	info = r.getUpdate(t, info.EgressId)
	if info.Status == livekit.EgressStatus_EGRESS_ACTIVE {
		r.checkUpdate(t, info.EgressId, livekit.EgressStatus_EGRESS_FAILED)
	} else {
		require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, info.Status)
	}
}

func (r *Runner) testTrackDisconnection(t *testing.T, test *testCase) {
	test.videoTrackID = r.publishSampleWithDisconnection(t, types.MimeTypeVP8)
	r.runFileTest(t, test)
}

func (r *Runner) testEmptyStreamBin(t *testing.T, test *testCase) {
	req := r.build(test)

	info := r.sendRequest(t, req)
	egressID := info.EgressId
	time.Sleep(time.Second * 15)

	// get params
	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		rtmpUrl4Redacted:    livekit.StreamInfo_ACTIVE,
		badRtmpUrl1Redacted: livekit.StreamInfo_FAILED,
	})
	_, err = r.client.UpdateStream(context.Background(), egressID, &livekit.UpdateStreamRequest{
		EgressId:         egressID,
		RemoveOutputUrls: []string{rtmpUrl4},
	})
	require.NoError(t, err)
	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		rtmpUrl4Redacted:    livekit.StreamInfo_FINISHED,
		badRtmpUrl1Redacted: livekit.StreamInfo_FAILED,
	})

	time.Sleep(time.Second * 10)
	res := r.stopEgress(t, egressID)
	r.verifySegments(t, test, p, livekit.SegmentedFileSuffix_INDEX, res, false)
}
