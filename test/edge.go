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

	// ParticipantComposite where the participant does not publish a track
	r.runParticipantTest(t, "6A/Edge/ParticipantNoPublish", &testCase{},
		func(t *testing.T, identity string) {
			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_Participant{
					Participant: &livekit.ParticipantEgressRequest{
						RoomName: r.room.Name(),
						Identity: identity,
						FileOutputs: []*livekit.EncodedFileOutput{{
							FileType: livekit.EncodedFileType_MP4,
							Filepath: "there won't be a file",
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

	// Stream output with a bad rtmp url or stream key
	r.runRoomTest(t, "6B/Edge/RtmpFailure", types.MimeTypeOpus, types.MimeTypeVP8, func(t *testing.T) {
		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &rpc.StartEgressRequest_RoomComposite{
				RoomComposite: &livekit.RoomCompositeEgressRequest{
					RoomName: r.RoomName,
					Layout:   "speaker-light",
					StreamOutputs: []*livekit.StreamOutput{{
						Protocol: livekit.StreamProtocol_RTMP,
						Urls:     []string{badStreamUrl1},
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

		// check update
		time.Sleep(time.Second * 5)
		info = r.getUpdate(t, info.EgressId)
		if info.Status == livekit.EgressStatus_EGRESS_ACTIVE {
			r.checkUpdate(t, info.EgressId, livekit.EgressStatus_EGRESS_FAILED)
		} else {
			require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, info.Status)
		}
	})

	// Track composite with data loss due to a disconnection
	t.Run("6C/Edge/TrackDisconnection", func(t *testing.T) {
		r.awaitIdle(t)

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
