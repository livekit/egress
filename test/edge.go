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

			// RoomComposite with a late-joining participant (audio only).
			// Verifies that file duration reflects wall-clock time, not
			// inflated by the late track's PTS offset.

			{
				name:        "RoomCompositeLateTrackDuration",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					audioOnly:  true,
				},
				fileOptions: &fileOptions{
					filename: "room_composite_late_track_{time}",
					fileType: livekit.EncodedFileType_OGG,
				},
				custom: r.testRoomCompositeLateTrackDuration,
			},

			// RoomComposite audio mixing

			{
				name:        "AudioMixing",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioOnly:   true,
					audioMixing: livekit.AudioMixing_DUAL_CHANNEL_AGENT,
					expectedAudioChannels: map[string]livekit.AudioChannel{
						"p0": livekit.AudioChannel_AUDIO_CHANNEL_LEFT,
						"p1": livekit.AudioChannel_AUDIO_CHANNEL_RIGHT,
						"p2": livekit.AudioChannel_AUDIO_CHANNEL_RIGHT,
					},
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

			// Room composite where all participants leave and the server
			// eventually disconnects the egress. Verifies that the reported
			// duration includes the silence tail between participant departure
			// and server-initiated leave.

			{
				name:        "RoomCompositeDisconnectDuration",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					audioOnly:  true,
				},
				fileOptions: &fileOptions{
					filename: "room_composite_disconnect_duration_{time}",
					fileType: livekit.EncodedFileType_OGG,
				},
				custom: r.testRoomCompositeDisconnectDuration,
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

			// RTMP output that goes silent mid-stream (network-level wedge).
			// Uses toxiproxy to break the TCP socket while leaving handshake intact.

			{
				name:        "RtmpSilentWedge",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeH264,
				},
				streamOptions: &streamOptions{
					// streamUrls populated by testRtmpSilentWedge once toxiproxy is set up.
					outputType: types.OutputTypeRTMP,
				},
				custom: r.testRtmpSilentWedge,
			},

			// RTMP output where the server rejects at the publish step
			// (protocol-level reject). Reproduces the rapid-reject loop that
			// can wedge rtmp2sink in prod.

			{
				name:        "RtmpPublishReject",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeH264,
				},
				streamOptions: &streamOptions{
					// streamUrls populated by testRtmpPublishReject.
					outputType: types.OutputTypeRTMP,
				},
				custom: r.testRtmpPublishReject,
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
					audioCodec:         types.MimeTypeOpus,
					videoCodec:         types.MimeTypeVP8,
					disconnectAt:       time.Second * 10,
					disconnectDuration: time.Second * 10,
				},
				fileOptions: &fileOptions{
					filename: "track_disconnection_{time}.mp4",
					fileType: livekit.EncodedFileType_MP4,
				},
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
					streamUrls: []string{rtmpUrl1, badRtmpUrl1},
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
			if !r.run(t, test) {
				return
			}
		}
	})
}

func (r *Runner) testRoomCompositeLateTrackDuration(t *testing.T, test *testCase) {
	// First participant publishes audio immediately via the plan.
	// Start egress, wait for it to become active, then connect a second participant
	// after a delay. Stop egress and verify that the reported file duration is close
	// to wall-clock time and not inflated by the late track's synchronizer offset.
	req := r.build(test)
	testStart := time.Now()
	egressID := r.startEgress(t, req)

	// Second participant joins several seconds after egress is active
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
	r.publish(t, p2.LocalParticipant, types.MimeTypeOpus)

	// Let the late track record for a few seconds
	time.Sleep(time.Second * 7)

	// Stop and verify
	res := r.stopEgress(t, egressID)
	wallClock := time.Since(testStart)

	fileRes := res.GetFile() //nolint:staticcheck
	if fileRes == nil {
		require.Len(t, res.FileResults, 1)
		fileRes = res.FileResults[0]
	}

	reportedDuration := time.Duration(fileRes.Duration)
	t.Logf("reported duration: %s, wall-clock: %s, startedAt: %d, endedAt: %d",
		reportedDuration, wallClock, fileRes.StartedAt, fileRes.EndedAt)

	// Reported duration must not exceed wall-clock time. It can legitimately be
	// shorter (pipeline startup delay between testStart and first packet), but
	// should never be longer.
	require.LessOrEqual(t, reportedDuration.Seconds(), wallClock.Seconds()+3.0,
		"file duration should not exceed wall-clock duration (inflated by late track offset)")
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

	// The default planSingleParticipant produces a p0 stub with no audio
	// events (no audioCodec set on this test case). Replace it with a plan
	// that reflects what we actually published — p0 (agent), p1, p2 each
	// emitting Opus — so verifyContent doesn't flag every detected beep as
	// "unexpected".
	publish := func(name string) *Publisher {
		return &Publisher{
			name:  name,
			audio: []Event{{kind: eventPublish, codec: types.MimeTypeOpus}},
		}
	}
	test.plan = &Plan{publishers: []*Publisher{publish("p0"), publish("p1"), publish("p2")}}

	r.executeTest(t, test)
}

func (r *Runner) testParticipantNoPublish(t *testing.T, test *testCase) {
	req := r.build(test)

	info := r.sendRequest(t, req)
	time.Sleep(time.Second * 15)
	test.publishers["p0"].room.Disconnect()
	time.Sleep(time.Second * 30)
	info = r.getUpdate(t, info.EgressId)
	require.Equal(t, livekit.EgressStatus_EGRESS_ABORTED.String(), info.Status.String())
}

func (r *Runner) testRoomCompositeStaysOpen(t *testing.T, test *testCase) {
	req := r.build(test)

	info := r.sendRequest(t, req)
	time.Sleep(time.Second * 10)

	p0 := test.publishers["p0"]
	p0.room.Disconnect()
	time.Sleep(time.Second * 10)

	// Reconnect with the same identity and republish so the egress can
	// observe both the gap and the resumption.
	rm, err := r.connectAs(test.plan.publishers[0], p0.identity, nil)
	require.NoError(t, err)
	p0.room = rm
	p0.lp = rm.LocalParticipant

	r.publish(t, p0.lp, types.MimeTypeOpus)
	r.publish(t, p0.lp, types.MimeTypeVP8)

	time.Sleep(time.Second * 10)

	r.checkUpdate(t, info.EgressId, livekit.EgressStatus_EGRESS_ACTIVE)
	r.stopEgress(t, info.EgressId)
}

func (r *Runner) testRoomCompositeDisconnectDuration(t *testing.T, test *testCase) {
	// Start egress, record for a while, then disconnect all participants.
	// The server will eventually disconnect the egress after departure_timeout.
	// The file will contain silence during that gap, so endedAt must
	// reflect the full file content including the silence tail.
	const departureTimeout = 20 // seconds

	// Create the room with an explicit departure_timeout so the silence
	// gap is predictable regardless of server defaults.
	roomClient := lksdk.NewRoomServiceClient(r.WsUrl, r.ApiKey, r.ApiSecret)
	_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:             r.RoomName,
		DepartureTimeout: departureTimeout,
	})
	require.NoError(t, err)

	req := r.build(test)
	egressID := r.startEgress(t, req)

	// Record with active audio for 10 seconds
	time.Sleep(time.Second * 10)

	// Disconnect all participants — the room becomes empty, but the
	// egress stays connected until the server kicks it out.
	disconnectTime := time.Now()
	test.publishers["p0"].room.Disconnect()

	// Wait for the egress to complete on its own (server-initiated leave).
	// Poll the latest snapshot until we see EGRESS_COMPLETE or EGRESS_FAILED.
	var res *livekit.EgressInfo
	deadline := time.Now().Add(90 * time.Second)
	for res == nil && time.Now().Before(deadline) {
		r.updates.Lock()
		info := r.updates.EgressInfo
		r.updates.Unlock()
		if info != nil && info.EgressId == egressID {
			switch info.Status {
			case livekit.EgressStatus_EGRESS_COMPLETE:
				res = info
			case livekit.EgressStatus_EGRESS_FAILED:
				t.Fatalf("egress failed: %s", info.Error)
			}
		}
		if res == nil {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if res == nil {
		t.Fatal("timed out waiting for egress to complete after room disconnect")
	}

	silenceGap := time.Since(disconnectTime)
	t.Logf("silence gap after disconnect: %s", silenceGap)

	fileRes := res.GetFile() //nolint:staticcheck
	if fileRes == nil {
		require.Len(t, res.FileResults, 1)
		fileRes = res.FileResults[0]
	}

	reportedDuration := time.Duration(fileRes.Duration)
	t.Logf("reported duration: %s, startedAt: %d, endedAt: %d",
		reportedDuration, fileRes.StartedAt, fileRes.EndedAt)

	// The reported duration should include the silence tail. The room was
	// created with departure_timeout=20s, so the server disconnects the
	// egress ~20s after the last participant leaves. We allow 5s of slack
	// for pipeline startup/teardown.
	minExpected := 10*time.Second + silenceGap - 5*time.Second
	require.GreaterOrEqual(t, reportedDuration, minExpected,
		"file duration should include silence tail after participants left")
}

func (r *Runner) testStorageLimit(t *testing.T, test *testCase) {
	origLimit := r.FileOutputMaxSize
	r.FileOutputMaxSize = 300000 // ~300KB to trigger quickly
	t.Cleanup(func() {
		r.FileOutputMaxSize = origLimit
	})

	req := r.build(test)
	info := r.sendRequest(t, req)
	egressID := info.EgressId

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		update := r.getUpdate(t, egressID)
		switch update.Status { //nolint:revive // EGRESS_ACTIVE explicitly listed for readability
		case livekit.EgressStatus_EGRESS_ACTIVE:
			time.Sleep(100 * time.Millisecond)
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
			time.Sleep(100 * time.Millisecond)
			continue
		}
	}
	t.Fatal("timed out waiting for storage limit")
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
		time.Sleep(100 * time.Millisecond)
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

func (r *Runner) testEmptyStreamBin(t *testing.T, test *testCase) {
	req := r.build(test)

	info := r.sendRequest(t, req)
	egressID := info.EgressId
	time.Sleep(time.Second * 15)

	// get params
	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		rtmpUrl1Redacted:    livekit.StreamInfo_ACTIVE,
		badRtmpUrl1Redacted: livekit.StreamInfo_FAILED,
	})
	_, err = r.client.UpdateStream(context.Background(), egressID, &livekit.UpdateStreamRequest{
		EgressId:         egressID,
		RemoveOutputUrls: []string{rtmpUrl1},
	})
	require.NoError(t, err)
	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		rtmpUrl1Redacted:    livekit.StreamInfo_FINISHED,
		badRtmpUrl1Redacted: livekit.StreamInfo_FAILED,
	})

	time.Sleep(time.Second * 10)
	res := r.stopEgress(t, egressID)
	r.verifySegments(t, test, p, livekit.SegmentedFileSuffix_INDEX, res, false)
}
