//go:build integration

package test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

func testRoomCompositeFile(t *testing.T, conf *TestConfig) {
	publishSamplesToRoom(t, conf.room, params.MimeTypeOpus, params.MimeTypeVP8, conf.Muting)

	now := time.Now().Unix()
	for _, test := range []*testCase{
		{
			name:     "h264-high-mp4",
			fileType: livekit.EncodedFileType_MP4,
			options: &livekit.EncodingOptions{
				AudioCodec:   livekit.AudioCodec_AAC,
				VideoCodec:   livekit.VideoCodec_H264_HIGH,
				Height:       720,
				Width:        1280,
				VideoBitrate: 4500,
			},
			filename: fmt.Sprintf("room-h264-high-%v.mp4", now),
		},
		{
			name:      "opus-ogg",
			fileType:  livekit.EncodedFileType_OGG,
			audioOnly: true,
			options: &livekit.EncodingOptions{
				AudioCodec: livekit.AudioCodec_OPUS,
			},
			filename: fmt.Sprintf("room-opus-%v.ogg", now),
		},
		{
			name:     "h264-high-mp4-limit",
			fileType: livekit.EncodedFileType_MP4,
			options: &livekit.EncodingOptions{
				AudioCodec:   livekit.AudioCodec_AAC,
				VideoCodec:   livekit.VideoCodec_H264_HIGH,
				Height:       720,
				Width:        1280,
				VideoBitrate: 4500,
			},
			filename:       fmt.Sprintf("room-h264-high-limit-%v.mp4", now),
			sessionTimeout: time.Second * 20,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)

			filepath := getFilePath(conf.Config, test.filename)
			roomRequest := &livekit.RoomCompositeEgressRequest{
				RoomName:  conf.room.Name(),
				Layout:    "speaker-dark",
				AudioOnly: test.audioOnly,
				Output: &livekit.RoomCompositeEgressRequest_File{
					File: &livekit.EncodedFileOutput{
						FileType: test.fileType,
						Filepath: filepath,
					},
				},
			}

			if test.options != nil {
				roomRequest.Options = &livekit.RoomCompositeEgressRequest_Advanced{
					Advanced: test.options,
				}
			}

			req := &livekit.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &livekit.StartEgressRequest_RoomComposite{
					RoomComposite: roomRequest,
				},
			}

			runFileTest(t, conf, req, test, filepath)
		})
	}
}

func testRoomCompositeStream(t *testing.T, conf *TestConfig) {
	publishSamplesToRoom(t, conf.room, params.MimeTypeOpus, params.MimeTypeVP8, conf.Muting)

	t.Run("rtmp-failure", func(t *testing.T) {
		awaitIdle(t, conf.svc)

		req := &livekit.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &livekit.StartEgressRequest_RoomComposite{
				RoomComposite: &livekit.RoomCompositeEgressRequest{
					RoomName: conf.RoomName,
					Layout:   "speaker-light",
					Output: &livekit.RoomCompositeEgressRequest_Stream{
						Stream: &livekit.StreamOutput{
							Protocol: livekit.StreamProtocol_RTMP,
							Urls:     []string{badStreamUrl},
						},
					},
				},
			},
		}

		info, err := conf.rpcClient.SendRequest(context.Background(), req)
		require.NoError(t, err)
		require.Empty(t, info.Error)
		require.NotEmpty(t, info.EgressId)
		require.Equal(t, conf.RoomName, info.RoomName)
		require.Equal(t, livekit.EgressStatus_EGRESS_STARTING, info.Status)

		// wait
		time.Sleep(time.Second * 5)

		info = getUpdate(t, conf.updates, info.EgressId)
		if info.Status == livekit.EgressStatus_EGRESS_ACTIVE {
			checkUpdate(t, conf.updates, info.EgressId, livekit.EgressStatus_EGRESS_FAILED)
		} else {
			require.Equal(t, info.Status, livekit.EgressStatus_EGRESS_FAILED)
		}
	})

	for _, test := range []*testCase{
		{
			name: "room-rtmp",
		},
		{
			name:           "room-rtmp-limit",
			sessionTimeout: time.Second * 20,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)

			req := &livekit.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &livekit.StartEgressRequest_RoomComposite{
					RoomComposite: &livekit.RoomCompositeEgressRequest{
						RoomName: conf.room.Name(),
						Layout:   "grid-light",
						Output: &livekit.RoomCompositeEgressRequest_Stream{
							Stream: &livekit.StreamOutput{
								Protocol: livekit.StreamProtocol_RTMP,
								Urls:     []string{streamUrl1},
							},
						},
					},
				},
			}

			runStreamTest(t, conf, req, test.sessionTimeout)
		})
	}
}

func testRoomCompositeSegments(t *testing.T, conf *TestConfig) {
	publishSamplesToRoom(t, conf.room, params.MimeTypeOpus, params.MimeTypeVP8, conf.Muting)

	now := time.Now().Unix()
	for _, test := range []*testCase{
		{
			name: "h264-segmented-mp4",
			options: &livekit.EncodingOptions{
				AudioCodec:   livekit.AudioCodec_AAC,
				VideoCodec:   livekit.VideoCodec_H264_BASELINE,
				Height:       1080,
				Width:        1920,
				VideoBitrate: 4500,
			},
			filename: fmt.Sprintf("room-h264-baseline-%v", now),
			playlist: fmt.Sprintf("room-h264-baseline-%v.m3u8", now),
		},
		{
			name: "h264-segmented-mp4-limit",
			options: &livekit.EncodingOptions{
				AudioCodec:   livekit.AudioCodec_AAC,
				VideoCodec:   livekit.VideoCodec_H264_BASELINE,
				Height:       1080,
				Width:        1920,
				VideoBitrate: 4500,
			},
			filename:       fmt.Sprintf("room-h264-baseline-limit-%v", now),
			playlist:       fmt.Sprintf("room-h264-baseline-limit-%v.m3u8", now),
			sessionTimeout: time.Second * 20,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)

			roomRequest := &livekit.RoomCompositeEgressRequest{
				RoomName:  conf.RoomName,
				Layout:    "grid-dark",
				AudioOnly: test.audioOnly,
				Output: &livekit.RoomCompositeEgressRequest_Segments{
					Segments: &livekit.SegmentedFileOutput{
						FilenamePrefix: getFilePath(conf.Config, test.filename),
						PlaylistName:   test.playlist,
					},
				},
			}

			if test.options != nil {
				roomRequest.Options = &livekit.RoomCompositeEgressRequest_Advanced{
					Advanced: test.options,
				}
			}

			req := &livekit.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &livekit.StartEgressRequest_RoomComposite{
					RoomComposite: roomRequest,
				},
			}

			runSegmentsTest(t, conf, req, getFilePath(conf.Config, test.playlist), test.sessionTimeout)
		})
	}
}
