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
	"path"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/source/pulse"
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

			// Room composite where the egress participant loses its room
			// connection with a retryable reason. The partial recording must
			// still be finalized and uploaded, and the egress reported FAILED
			// so it could be retried.

			{
				name:        "RoomCompositeRetryableDisconnect",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					audioOnly:  true,
				},
				fileOptions: &fileOptions{
					filename: "retryable_disconnect_{time}",
					fileType: livekit.EncodedFileType_OGG,
				},
				custom: r.testRoomCompositeRetryableDisconnect,
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

			// Another participant joins the room with the egress's identity
			// and evicts it (DisconnectReason_DUPLICATE_IDENTITY). The egress
			// must exit silently — no terminal UpdateEgress reaches the io
			// server, since another worker is presumed to still be running
			// the same egressID.

			{
				name:        "DuplicateIdentitySilentExit",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					audioOnly:  true,
				},
				fileOptions: &fileOptions{
					filename: "duplicate_identity_{time}",
					fileType: livekit.EncodedFileType_OGG,
				},
				custom: r.testDuplicateIdentitySilentExit,
			},

			// Handler killed without cleanup (crash, OOM-kill) leaks its
			// null-sink on the shared pulse daemon. The monitor's reaper
			// must unload it once the egress is no longer active.

			{
				name:        "PulseSinkReaper",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeVP8,
				},
				fileOptions: &fileOptions{
					filename: "pulse_sink_reaper_{time}.mp4",
				},
				custom: r.testPulseSinkReaper,
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

func (r *Runner) testDuplicateIdentitySilentExit(t *testing.T, test *testCase) {
	req := r.build(test)
	egressID := req.EgressId

	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	r.startEgress(t, req)

	// Snapshot the latest update before the duplicate joins. After the
	// silent exit, the egress instance must not emit any further updates —
	// not COMPLETE, not FAILED, not even an intermediate ENDING — since
	// another instance owns the recording.
	startUpdate := r.getUpdate(t, egressID)
	require.NotEmpty(t, startUpdate.FileResults)
	storagePath := startUpdate.FileResults[0].Filename
	require.NotEmpty(t, storagePath, "storage filename should be resolved at start")

	preEvictStatus := startUpdate.Status
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE, preEvictStatus,
		"snapshot must be ACTIVE before the duplicate joins")

	// Join the room with the egress's own identity. The SFU evicts the
	// older session with DisconnectReason_DUPLICATE_IDENTITY.
	dup, err := lksdk.ConnectToRoom(r.WsUrl, lksdk.ConnectInfo{
		APIKey:              r.ApiKey,
		APISecret:           r.ApiSecret,
		RoomName:            r.RoomName,
		ParticipantName:     "duplicate-egress",
		ParticipantIdentity: egressID,
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	t.Cleanup(dup.Disconnect)

	// Wait for the handler subprocess to exit on its own (no KillAll —
	// the eviction itself must drive the shutdown).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if r.svc.IsIdle() {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.True(t, r.svc.IsIdle(), "egress handler should exit after duplicate-identity eviction")

	// Give a small grace period for any in-flight UpdateEgress IPC to land.
	time.Sleep(2 * time.Second)

	last := r.getUpdate(t, egressID)
	require.Equalf(t, preEvictStatus, last.Status,
		"no UpdateEgress should have moved status after duplicate-identity eviction (was %s, now %s)",
		preEvictStatus, last.Status)

	// And the output file must not have landed in storage — another
	// egress instance is presumed to own the destination.
	requireNotUploaded(t, p.GetFileConfig().StorageConfig, storagePath)
}

func (r *Runner) testRoomCompositeRetryableDisconnect(t *testing.T, test *testCase) {
	// Inject a retryable disconnect into this egress once it is active. Tests
	// run serially, so scoping the override to the current room and clearing it
	// on cleanup keeps it isolated to this test.
	original := r.TestOverrides.DisconnectInjectionRoom
	r.TestOverrides.DisconnectInjectionRoom = r.RoomName
	t.Cleanup(func() { r.TestOverrides.DisconnectInjectionRoom = original })

	req := r.build(test)
	egressID := req.EgressId

	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	r.startEgress(t, req)

	// The injected disconnect should drive the egress to finalize and exit.
	require.Eventually(t, r.svc.IsIdle, 45*time.Second, 200*time.Millisecond,
		"egress should finalize and exit after retryable disconnect")

	// Give a small grace period for the terminal UpdateEgress to land.
	time.Sleep(2 * time.Second)

	res := r.getUpdate(t, egressID)

	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, res.Status)
	require.Contains(t, res.Error, "connection to room failed")

	// But the partial recording is still finalized and uploaded.
	require.Len(t, res.FileResults, 1)
	fileRes := res.FileResults[0]
	require.NotEmpty(t, fileRes.Location, "partial file should be uploaded despite FAILED status")
	require.Greater(t, fileRes.Size, int64(0))
	require.Greater(t, fileRes.Duration, int64(0))

	// And it is downloadable from storage.
	local := fmt.Sprintf("%s/retryable_disconnect_partial.ogg", r.FilePrefix)
	download(t, p.GetFileConfig().StorageConfig, local, fileRes.Filename, true)
}

func (r *Runner) testPulseSinkReaper(t *testing.T, test *testCase) {
	req := r.build(test)
	egressID := r.startEgress(t, req)

	// chrome records through a null-sink named after the egress
	require.Eventually(t, func() bool {
		loaded, err := egressSinkLoaded(egressID)
		return err == nil && loaded
	}, 30*time.Second, time.Second, "pulse sink should be loaded while the egress is running")

	// the reaper's gauge picks up the loaded sink on its next tick
	require.Eventually(t, func() bool {
		return pulseSinksGauge(t) >= 1
	}, 30*time.Second, time.Second, "pulse_sinks gauge should count the loaded sink")

	// SIGKILL the handler so WebSource.Close never runs and the sink leaks
	pid := findHandlerPID(t, egressID)
	t.Cleanup(func() {
		// sweep the orphaned chrome/xvfb the dead handler left behind
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	})
	require.NoError(t, syscall.Kill(pid, syscall.SIGKILL))

	// the service notices the death, reports the egress failed, and drops it
	// from the monitor, leaving the sink orphaned
	deadline := time.Now().Add(30 * time.Second)
	for r.getUpdate(t, egressID).Status != livekit.EgressStatus_EGRESS_FAILED {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the killed egress to be reported failed")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// the reaper unloads the orphaned sink after the grace period
	require.Eventually(t, func() bool {
		loaded, err := egressSinkLoaded(egressID)
		return err == nil && !loaded
	}, time.Minute, time.Second, "orphaned pulse sink should be reaped")

	// and the gauge drops back to zero on the tick after the unload
	require.Eventually(t, func() bool {
		return pulseSinksGauge(t) == 0
	}, 30*time.Second, time.Second, "pulse_sinks gauge should drop after the reap")
}

// pulseSinksGauge reads livekit_egress_pulse_sinks from the default prometheus
// registry, which the in-process monitor registers into. Returns -1 if the
// gauge isn't registered or gathering fails.
func pulseSinksGauge(t *testing.T) float64 {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Logf("prometheus gather failed: %v", err)
		return -1
	}

	for _, family := range families {
		if family.GetName() == "livekit_egress_pulse_sinks" {
			for _, m := range family.GetMetric() {
				return m.GetGauge().GetValue()
			}
		}
	}
	return -1
}

func egressSinkLoaded(egressID string) (bool, error) {
	info, err := pulse.List()
	if err != nil {
		return false, err
	}
	for _, sink := range info.Sinks {
		if sink.Name == egressID {
			return true, nil
		}
	}
	return false, nil
}

// findHandlerPID locates the `egress run-handler` process for the given egress
// by scanning /proc, since the ProcessManager doesn't expose handler PIDs.
func findHandlerPID(t *testing.T, egressID string) int {
	entries, err := os.ReadDir("/proc")
	require.NoError(t, err)

	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(path.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := string(cmdline)
		if strings.Contains(args, "run-handler") && strings.Contains(args, egressID) {
			return pid
		}
	}

	t.Fatalf("no run-handler process found for %s", egressID)
	return 0
}
