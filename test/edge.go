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

package test

import (
	"context"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func (r *Runner) testEdgeCases(t *testing.T) {
	if !r.should(runEdge) {
		return
	}

	t.Run("EdgeCases", func(t *testing.T) {
		r.testParticipantNoPublish(t)
		r.testRoomCompositeStaysOpen(t)
		r.testRtmpFailure(t)
		r.testSrtFailure(t)
		r.testTrackDisconnection(t)
		r.testEmptyStreamBin(t)
	})
}

// ParticipantComposite where the participant never publishes
func (r *Runner) testParticipantNoPublish(t *testing.T) {
	r.runParticipantTest(t, "ParticipantNoPublish", &testCase{},
		func(t *testing.T, identity string) {
			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_Participant{
					Participant: &livekit.ParticipantEgressRequest{
						RoomName: r.room.Name(),
						Identity: identity,
						FileOutputs: []*livekit.EncodedFileOutput{{
							FileType: livekit.EncodedFileType_MP4,
						}},
					},
				},
			}

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
		},
	)
}

// Test that the egress continues if a user leaves
func (r *Runner) testRoomCompositeStaysOpen(t *testing.T) {
	r.run(t, "RoomCompositeStaysOpen", func(t *testing.T) {
		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &rpc.StartEgressRequest_RoomComposite{
				RoomComposite: &livekit.RoomCompositeEgressRequest{
					RoomName: r.RoomName,
					Layout:   "speaker",
					FileOutputs: []*livekit.EncodedFileOutput{{
						Filepath: path.Join(r.FilePrefix, "room_composite_duration_{time}.mp4"),
					}},
				},
			},
		}

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

		r.publishSamples(t, types.MimeTypeOpus, types.MimeTypeVP8)
		time.Sleep(time.Second * 10)

		r.checkUpdate(t, info.EgressId, livekit.EgressStatus_EGRESS_ACTIVE)
		r.stopEgress(t, info.EgressId)
	})
}

// RTMP output with no valid urls
func (r *Runner) testRtmpFailure(t *testing.T) {
	r.runRoomTest(t, "RtmpFailure", types.MimeTypeOpus, types.MimeTypeVP8, func(t *testing.T) {
		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &rpc.StartEgressRequest_RoomComposite{
				RoomComposite: &livekit.RoomCompositeEgressRequest{
					RoomName: r.RoomName,
					Layout:   "speaker-light",
					StreamOutputs: []*livekit.StreamOutput{{
						Protocol: livekit.StreamProtocol_RTMP,
						Urls:     []string{badRtmpUrl1},
					}},
				},
			},
		}

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
	})
}

// SRT output with a no valid urls
func (r *Runner) testSrtFailure(t *testing.T) {
	r.run(t, "SrtFailure", func(t *testing.T) {
		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &rpc.StartEgressRequest_Web{
				Web: &livekit.WebEgressRequest{
					Url: webUrl,
					StreamOutputs: []*livekit.StreamOutput{{
						Protocol: livekit.StreamProtocol_SRT,
						Urls:     []string{badSrtUrl1},
					}},
				},
			},
		}

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
	})

}

// Track composite with data loss due to a disconnection
func (r *Runner) testTrackDisconnection(t *testing.T) {
	r.run(t, "TrackDisconnection", func(t *testing.T) {
		test := &testCase{
			fileType:   livekit.EncodedFileType_MP4,
			audioCodec: types.MimeTypeOpus,
			videoCodec: types.MimeTypeVP8,
			filename:   "track_disconnection_{time}.mp4",
		}

		audioTrackID := r.publishSample(t, test.audioCodec, false)
		videoTrackID := r.publishSampleWithDisconnection(t, test.videoCodec)

		var fileOutput *livekit.EncodedFileOutput
		if r.AzureUpload != nil {
			fileOutput = &livekit.EncodedFileOutput{
				FileType: test.fileType,
				Filepath: path.Join(uploadPrefix, test.filename),
				Output: &livekit.EncodedFileOutput_Azure{
					Azure: r.AzureUpload,
				},
			}
		} else {
			fileOutput = &livekit.EncodedFileOutput{
				FileType: test.fileType,
				Filepath: path.Join(r.FilePrefix, test.filename),
			}
		}

		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &rpc.StartEgressRequest_TrackComposite{
				TrackComposite: &livekit.TrackCompositeEgressRequest{
					RoomName:     r.room.Name(),
					AudioTrackId: audioTrackID,
					VideoTrackId: videoTrackID,
					FileOutputs:  []*livekit.EncodedFileOutput{fileOutput},
				},
			},
		}

		test.expectVideoEncoding = true
		r.runFileTest(t, req, test)
	})
}

func (r *Runner) testEmptyStreamBin(t *testing.T) {
	r.runRoomTest(t, "Multi", types.MimeTypeOpus, types.MimeTypeVP8, func(t *testing.T) {
		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &rpc.StartEgressRequest_RoomComposite{
				RoomComposite: &livekit.RoomCompositeEgressRequest{
					RoomName: r.room.Name(),
					Layout:   "grid-light",
					StreamOutputs: []*livekit.StreamOutput{{
						Urls: []string{rtmpUrl1, badRtmpUrl1},
					}},
					SegmentOutputs: []*livekit.SegmentedFileOutput{{
						FilenamePrefix: path.Join(r.FilePrefix, "empty_stream_{time}"),
						PlaylistName:   "empty_stream_{time}",
					}},
				},
			},
		}

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
		r.verifySegments(t, p, livekit.SegmentedFileSuffix_INDEX, res, false)
	})
}
