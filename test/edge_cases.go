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
	"github.com/livekit/protocol/rpc"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

var edgeTests = []*testCase{
	{
		name: "EdgeCase/RoomCompositeLateTrackDuration",
		options: []Mutation{
			RequestTypes.RoomCompositeAudioOnly.Apply,
			func(tc *TestConfig) {
				tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{
					Filename: "room_composite_late_track_{time}",
					FileType: livekit.EncodedFileType_OGG,
				})
			},
		},
		custom: (*Runner).testRoomCompositeLateTrackDuration,
	},
	{
		name: "EdgeCase/Agents",
		options: []Mutation{
			func(tc *TestConfig) {
				tc.RequestType = types.RequestTypeRoomComposite
			},
			func(tc *TestConfig) {
				tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{Filename: "agents_{time}"})
			},
		},
		custom: (*Runner).testAgents,
	},
	{
		name: "EdgeCase/AudioMixing",
		options: []Mutation{
			func(tc *TestConfig) {
				tc.RequestType = types.RequestTypeRoomComposite
				tc.AudioOnly = true
				tc.AudioMixing = livekit.AudioMixing_DUAL_CHANNEL_AGENT
			},
			func(tc *TestConfig) {
				tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{Filename: "audio_mixing_{time}"})
			},
		},
		custom: (*Runner).testAudioMixing,
	},
	{
		name: "EdgeCase/ParticipantNoPublish",
		options: []Mutation{
			func(tc *TestConfig) {
				tc.RequestType = types.RequestTypeParticipant
			},
			func(tc *TestConfig) {
				tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{
					Filename: "participant_no_publish_{time}.mp4",
					FileType: livekit.EncodedFileType_MP4,
				})
			},
		},
		custom: (*Runner).testParticipantNoPublish,
	},
	{
		name: "EdgeCase/RoomCompositeStaysOpen",
		options: []Mutation{
			func(tc *TestConfig) {
				tc.RequestType = types.RequestTypeRoomComposite
			},
			func(tc *TestConfig) {
				tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{
					Filename: "room_composite_stays_open_{time}.mp4",
					FileType: livekit.EncodedFileType_MP4,
				})
			},
		},
		custom: (*Runner).testRoomCompositeStaysOpen,
	},
	{
		name: "EdgeCase/RoomCompositeDisconnectDuration",
		options: []Mutation{
			RequestTypes.RoomCompositeAudioOnly.Apply,
			func(tc *TestConfig) {
				tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{
					Filename: "room_composite_disconnect_duration_{time}",
					FileType: livekit.EncodedFileType_OGG,
				})
			},
		},
		custom: (*Runner).testRoomCompositeDisconnectDuration,
	},
	{
		name: "EdgeCase/RtmpFailure",
		options: []Mutation{
			func(tc *TestConfig) {
				tc.RequestType = types.RequestTypeRoomComposite
				tc.AudioCodec = types.MimeTypeOpus
				tc.VideoCodec = types.MimeTypeH264
			},
			func(tc *TestConfig) {
				tc.StreamOutputs = append(tc.StreamOutputs, streamOutputConfig{
					Protocol: livekit.StreamProtocol_RTMP,
					Urls:     []string{badRtmpUrl1},
				})
			},
		},
		custom: (*Runner).testRtmpFailure,
	},
	{
		name: "EdgeCase/SrtFailure",
		options: []Mutation{
			func(tc *TestConfig) {
				tc.RequestType = types.RequestTypeWeb
			},
			func(tc *TestConfig) {
				tc.StreamOutputs = append(tc.StreamOutputs, streamOutputConfig{
					Protocol: livekit.StreamProtocol_SRT,
					Urls:     []string{badSrtUrl1},
				})
			},
		},
		custom: (*Runner).testSrtFailure,
	},
	{
		name: "EdgeCase/TrackDisconnection",
		options: []Mutation{
			func(tc *TestConfig) {
				tc.RequestType = types.RequestTypeTrackComposite
				tc.AudioCodec = types.MimeTypeOpus
			},
			func(tc *TestConfig) {
				tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{
					Filename: "track_disconnection_{time}.mp4",
					FileType: livekit.EncodedFileType_MP4,
				})
			},
		},
		custom: (*Runner).testTrackDisconnection,
	},
	{
		name: "EdgeCase/EmptyStreamBin",
		options: []Mutation{
			func(tc *TestConfig) {
				tc.RequestType = types.RequestTypeRoomComposite
				tc.AudioCodec = types.MimeTypeOpus
				tc.VideoCodec = types.MimeTypeVP8
			},
			func(tc *TestConfig) {
				tc.StreamOutputs = append(tc.StreamOutputs, streamOutputConfig{
					Protocol: livekit.StreamProtocol_RTMP,
					Urls:     []string{rtmpUrl4, badRtmpUrl1},
				})
				tc.SegmentOutputs = append(tc.SegmentOutputs, segmentOutputConfig{
					Prefix:   "empty_stream_{time}",
					Playlist: "empty_stream_{time}",
				})
			},
		},
		custom: (*Runner).testEmptyStreamBin,
	},
	{
		name: "EdgeCase/FileStorageLimit",
		options: []Mutation{
			func(tc *TestConfig) {
				tc.RequestType = types.RequestTypeRoomComposite
				tc.AudioCodec = types.MimeTypeOpus
				tc.VideoCodec = types.MimeTypeVP8
			},
			func(tc *TestConfig) {
				tc.FileOutputs = append(tc.FileOutputs, fileOutputConfig{
					Filename: "storage_limit_{time}.mp4",
					FileType: livekit.EncodedFileType_MP4,
				})
			},
		},
		custom: (*Runner).testStorageLimit,
	},
}

// Edge case handler implementations.
// Signatures: func(r *Runner) handler(t *testing.T, req *rpc.StartEgressRequest, cfg *TestConfig)

func (r *Runner) testRoomCompositeLateTrackDuration(t *testing.T, req *rpc.StartEgressRequest, _ *TestConfig) {
	testStart := time.Now()
	egressID := r.startEgress(t, req)

	time.Sleep(time.Second * 5)

	p2, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "egress-late-joiner",
		ParticipantIdentity: fmt.Sprintf("late-joiner-%d", rand.Intn(100)),
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	t.Cleanup(p2.Disconnect)
	r.publish(t, p2.LocalParticipant, types.MimeTypeOpus, make(chan struct{}))

	time.Sleep(time.Second * 7)

	res := r.stopEgress(t, egressID)
	wallClock := time.Since(testStart)

	fileRes := res.GetFile() //nolint:staticcheck
	if fileRes == nil {
		require.Len(t, res.FileResults, 1)
		fileRes = res.FileResults[0]
	}

	reportedDuration := time.Duration(fileRes.Duration)
	t.Logf("reported duration: %s, wall-clock: %s", reportedDuration, wallClock)

	require.LessOrEqual(t, reportedDuration.Seconds(), wallClock.Seconds()+3.0,
		"file duration should not exceed wall-clock duration")
}

func (r *Runner) testAgents(t *testing.T, req *rpc.StartEgressRequest, cfg *TestConfig) {
	_, err := os.Stat("/agents/.env")
	if err != nil {
		t.Skip("skipping agents test; missing env file")
	}

	r.launchAgents(t)
	time.Sleep(time.Second * 5)
	r.runStandardTest(t, cfg, req)
}

func (r *Runner) testAudioMixing(t *testing.T, req *rpc.StartEgressRequest, cfg *TestConfig) {
	p1, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "egress-sample-1",
		ParticipantIdentity: fmt.Sprintf("sample-1-%d", rand.Intn(100)),
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	t.Cleanup(p1.Disconnect)
	r.publishForParticipant(t, p1.LocalParticipant, "p1", types.MimeTypeOpus)

	agent, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "egress-sample-agent",
		ParticipantIdentity: fmt.Sprintf("agent-%d", rand.Intn(100)),
		ParticipantKind:     lksdk.ParticipantAgent,
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	t.Cleanup(agent.Disconnect)
	r.publishForParticipant(t, agent.LocalParticipant, "p0", types.MimeTypeOpus)

	p2, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "egress-sample-2",
		ParticipantIdentity: fmt.Sprintf("sample-2-%d", rand.Intn(100)),
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	t.Cleanup(p2.Disconnect)
	r.publishForParticipant(t, p2.LocalParticipant, "p2", types.MimeTypeOpus)

	r.runStandardTest(t, cfg, req)
}

func (r *Runner) testParticipantNoPublish(t *testing.T, req *rpc.StartEgressRequest, _ *TestConfig) {
	identity := r.room.LocalParticipant.Identity()

	info := r.sendRequest(t, req)
	time.Sleep(time.Second * 15)
	r.room.Disconnect()
	time.Sleep(time.Second * 30)
	info = r.getUpdate(t, info.EgressId)
	require.Equal(t, livekit.EgressStatus_EGRESS_ABORTED.String(), info.Status.String())

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

func (r *Runner) testRoomCompositeStaysOpen(t *testing.T, req *rpc.StartEgressRequest, _ *TestConfig) {
	info := r.sendRequest(t, req)
	time.Sleep(time.Second * 10)
	identity := r.room.LocalParticipant.Identity()
	r.room.Disconnect()
	time.Sleep(time.Second * 10)

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

func (r *Runner) testRoomCompositeDisconnectDuration(t *testing.T, req *rpc.StartEgressRequest, _ *TestConfig) {
	const departureTimeout = 20

	roomClient := lksdk.NewRoomServiceClient(r.WsUrl, r.ApiKey, r.ApiSecret)
	_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:             r.RoomName,
		DepartureTimeout: departureTimeout,
	})
	require.NoError(t, err)

	egressID := r.startEgress(t, req)

	time.Sleep(time.Second * 10)

	disconnectTime := time.Now()
	identity := r.room.LocalParticipant.Identity()
	r.room.Disconnect()

	defer func() {
		room, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
			APIKey:              r.ApiKey,
			APISecret:           r.ApiSecret,
			RoomName:            r.RoomName,
			ParticipantName:     "egress-sample",
			ParticipantIdentity: identity,
		}, lksdk.NewRoomCallback())
		require.NoError(t, err)
		r.room = room
	}()

	var res *livekit.EgressInfo
	deadline := time.After(90 * time.Second)
	for res == nil {
		select {
		case info := <-r.updates:
			if info.EgressId != egressID {
				continue
			}
			switch info.Status {
			case livekit.EgressStatus_EGRESS_COMPLETE:
				res = info
			case livekit.EgressStatus_EGRESS_FAILED:
				t.Fatalf("egress failed: %s", info.Error)
			}
		case <-deadline:
			t.Fatal("timed out waiting for egress to complete after room disconnect")
		}
	}

	silenceGap := time.Since(disconnectTime)
	t.Logf("silence gap after disconnect: %s", silenceGap)

	fileRes := res.GetFile() //nolint:staticcheck
	if fileRes == nil {
		require.Len(t, res.FileResults, 1)
		fileRes = res.FileResults[0]
	}

	reportedDuration := time.Duration(fileRes.Duration)
	t.Logf("reported duration: %s", reportedDuration)

	minExpected := 10*time.Second + silenceGap - 5*time.Second
	require.GreaterOrEqual(t, reportedDuration, minExpected,
		"file duration should include silence tail after participants left")
}

func (r *Runner) testStorageLimit(t *testing.T, req *rpc.StartEgressRequest, _ *TestConfig) {
	origLimit := r.FileOutputMaxSize
	r.FileOutputMaxSize = 300000
	t.Cleanup(func() {
		r.FileOutputMaxSize = origLimit
	})

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
		case livekit.EgressStatus_EGRESS_LIMIT_REACHED:
			file := update.GetFile() //nolint:staticcheck
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

func (r *Runner) testRtmpFailure(t *testing.T, req *rpc.StartEgressRequest, _ *TestConfig) {
	info, err := r.StartEgress(context.Background(), req)
	require.NoError(t, err)
	require.Empty(t, info.Error)
	require.NotEmpty(t, info.EgressId)
	require.Equal(t, r.RoomName, info.RoomName)
	require.Equal(t, livekit.EgressStatus_EGRESS_STARTING, info.Status)

	time.Sleep(time.Second * 5)
	info = r.getUpdate(t, info.EgressId)
	streamFailed := false
	for info.Status == livekit.EgressStatus_EGRESS_ACTIVE {
		if !streamFailed && info.StreamResults[0].Status == livekit.StreamInfo_FAILED {
			streamFailed = true
		}
		if streamFailed {
			require.Equal(t, livekit.StreamInfo_FAILED, info.StreamResults[0].Status)
		}
		info = r.getUpdate(t, info.EgressId)
	}

	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, info.Status)
	require.NotEmpty(t, info.Error)
	require.Equal(t, livekit.StreamInfo_FAILED, info.StreamResults[0].Status)
	require.NotEmpty(t, info.StreamResults[0].Error)
}

func (r *Runner) testSrtFailure(t *testing.T, req *rpc.StartEgressRequest, _ *TestConfig) {
	info, err := r.StartEgress(context.Background(), req)
	require.NoError(t, err)
	require.Empty(t, info.Error)
	require.NotEmpty(t, info.EgressId)
	require.Equal(t, livekit.EgressStatus_EGRESS_STARTING, info.Status)

	time.Sleep(time.Second * 5)
	info = r.getUpdate(t, info.EgressId)
	if info.Status == livekit.EgressStatus_EGRESS_ACTIVE {
		r.checkUpdate(t, info.EgressId, livekit.EgressStatus_EGRESS_FAILED)
	} else {
		require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, info.Status)
	}
}

func (r *Runner) testTrackDisconnection(t *testing.T, req *rpc.StartEgressRequest, cfg *TestConfig) {
	// Publish VP8 with simulated disconnection, then inject track ID into request
	pub := r.publish(t, r.room.LocalParticipant, types.MimeTypeVP8, make(chan struct{}))
	videoTrackID := pub.SID()
	time.AfterFunc(time.Second*10, func() {
		pub.SimulateDisconnection(time.Second * 10)
	})

	// Inject into TrackComposite request
	if tc := req.GetTrackComposite(); tc != nil {
		tc.VideoTrackId = videoTrackID
	}

	r.runStandardTest(t, cfg, req)
}

func (r *Runner) testEmptyStreamBin(t *testing.T, req *rpc.StartEgressRequest, cfg *TestConfig) {
	info := r.sendRequest(t, req)
	egressID := info.EgressId
	time.Sleep(time.Second * 15)

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
	r.verifySegments(t, cfg, p, livekit.SegmentedFileSuffix_INDEX, res, false)
}
