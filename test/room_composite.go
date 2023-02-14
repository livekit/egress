//go:build integration

package test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

func testRoomCompositeFile(t *testing.T, conf *TestConfig) {
	publishSamplesToRoom(t, conf.room, types.MimeTypeOpus, types.MimeTypeH264, conf.Muting)

	for _, test := range []*testCase{
		{
			name:     "h264-high-mp4",
			fileType: livekit.EncodedFileType_MP4,
			options: &livekit.EncodingOptions{
				AudioCodec: livekit.AudioCodec_AAC,
				VideoCodec: livekit.VideoCodec_H264_HIGH,
			},
			filename:               "r_{room_name}_high_{time}.mp4",
			expectVideoTranscoding: true,
		},
		{
			name:     "h264-high-mp4-limit",
			fileType: livekit.EncodedFileType_MP4,
			options: &livekit.EncodingOptions{
				AudioCodec:   livekit.AudioCodec_AAC,
				Width:        1280,
				Height:       720,
				VideoBitrate: 4500,
			},
			filename:               "r_limit_{time}.mp4",
			sessionTimeout:         time.Second * 20,
			expectVideoTranscoding: true,
		},
		{
			name:      "opus-ogg",
			fileType:  livekit.EncodedFileType_OGG,
			audioOnly: true,
			options: &livekit.EncodingOptions{
				AudioCodec: livekit.AudioCodec_OPUS,
			},
			filename:               "r_{room_name}_opus_{time}",
			expectVideoTranscoding: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)

			fileOutput := &livekit.EncodedFileOutput{
				FileType: test.fileType,
				Filepath: getFilePath(conf.ServiceConfig, test.filename),
			}

			if conf.S3Upload != nil {
				fileOutput.Filepath = test.filename
				fileOutput.Output = &livekit.EncodedFileOutput_S3{
					S3: conf.S3Upload,
				}
			}

			roomRequest := &livekit.RoomCompositeEgressRequest{
				RoomName:  conf.room.Name(),
				Layout:    "speaker-dark",
				AudioOnly: test.audioOnly,
			}

			if conf.V2 {
				roomRequest.FileOutputs = []*livekit.EncodedFileOutput{fileOutput}
			} else {
				roomRequest.Output = &livekit.RoomCompositeEgressRequest_File{
					File: fileOutput,
				}
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

			runFileTest(t, conf, req, test)
		})
		if conf.Short {
			return
		}
	}
}

func testRoomCompositeStream(t *testing.T, conf *TestConfig) {
	publishSamplesToRoom(t, conf.room, types.MimeTypeOpus, types.MimeTypeVP8, conf.Muting)

	for _, test := range []*testCase{
		{
			name:                   "room-rtmp",
			expectVideoTranscoding: true,
		},
		{
			name:                   "room-rtmp-limit",
			sessionTimeout:         time.Second * 20,
			expectVideoTranscoding: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)

			room := &livekit.RoomCompositeEgressRequest{
				RoomName: conf.room.Name(),
				Layout:   "grid-light",
			}

			if conf.V2 {
				room.StreamOutputs = []*livekit.StreamOutput{{
					Protocol: livekit.StreamProtocol_RTMP,
					Urls:     []string{streamUrl1},
				}}
			} else {
				room.Output = &livekit.RoomCompositeEgressRequest_Stream{
					Stream: &livekit.StreamOutput{
						Protocol: livekit.StreamProtocol_RTMP,
						Urls:     []string{streamUrl1},
					},
				}
			}

			req := &livekit.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &livekit.StartEgressRequest_RoomComposite{
					RoomComposite: room,
				},
			}

			runStreamTest(t, conf, req, test)
		})
		if conf.Short {
			return
		}
	}

	t.Run("rtmp-failure", func(t *testing.T) {
		awaitIdle(t, conf.svc)

		room := &livekit.RoomCompositeEgressRequest{
			RoomName: conf.RoomName,
			Layout:   "speaker-light",
		}

		if conf.V2 {
			room.StreamOutputs = []*livekit.StreamOutput{{
				Protocol: livekit.StreamProtocol_RTMP,
				Urls:     []string{badStreamUrl},
			}}
		} else {
			room.Output = &livekit.RoomCompositeEgressRequest_Stream{
				Stream: &livekit.StreamOutput{
					Protocol: livekit.StreamProtocol_RTMP,
					Urls:     []string{badStreamUrl},
				},
			}
		}

		req := &livekit.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &livekit.StartEgressRequest_RoomComposite{
				RoomComposite: room,
			},
		}

		var info *livekit.EgressInfo
		var err error
		if conf.PSRPC {
			info, err = conf.psrpcClient.StartEgress(context.Background(), "", req)
		} else {
			info, err = conf.rpcClient.SendRequest(context.Background(), req)
		}
		require.NoError(t, err)
		require.Empty(t, info.Error)
		require.NotEmpty(t, info.EgressId)
		require.Equal(t, conf.RoomName, info.RoomName)
		require.Equal(t, livekit.EgressStatus_EGRESS_STARTING, info.Status)

		// wait
		time.Sleep(time.Second * 5)

		info = getUpdate(t, conf, info.EgressId)
		if info.Status == livekit.EgressStatus_EGRESS_ACTIVE {
			checkUpdate(t, conf, info.EgressId, livekit.EgressStatus_EGRESS_FAILED)
		} else {
			require.Equal(t, info.Status, livekit.EgressStatus_EGRESS_FAILED)
		}
	})
}

func testRoomCompositeSegments(t *testing.T, conf *TestConfig) {
	publishSamplesToRoom(t, conf.room, types.MimeTypeOpus, types.MimeTypeVP8, conf.Muting)

	for _, test := range []*testCase{
		{
			name: "rs-baseline",
			options: &livekit.EncodingOptions{
				AudioCodec:   livekit.AudioCodec_AAC,
				VideoCodec:   livekit.VideoCodec_H264_BASELINE,
				Width:        1920,
				Height:       1080,
				VideoBitrate: 4500,
			},
			filename:               "rs_{room_name}_{time}",
			playlist:               "rs_{room_name}_{time}.m3u8",
			filenameSuffix:         livekit.SegmentedFileSuffix_TIMESTAMP,
			expectVideoTranscoding: true,
		},
		{
			name: "rs-limit",
			options: &livekit.EncodingOptions{
				AudioCodec:   livekit.AudioCodec_AAC,
				VideoCodec:   livekit.VideoCodec_H264_BASELINE,
				Width:        1920,
				Height:       1080,
				VideoBitrate: 4500,
			},
			filename:               "rs_limit_{time}",
			playlist:               "rs_limit_{time}.m3u8",
			sessionTimeout:         time.Second * 20,
			expectVideoTranscoding: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)

			room := &livekit.RoomCompositeEgressRequest{
				RoomName:  conf.RoomName,
				Layout:    "grid-dark",
				AudioOnly: test.audioOnly,
			}

			if conf.V2 {
				room.SegmentOutputs = []*livekit.SegmentedFileOutput{{
					FilenamePrefix: getFilePath(conf.ServiceConfig, test.filename),
					PlaylistName:   test.playlist,
					FilenameSuffix: test.filenameSuffix,
				}}
			} else {
				room.Output = &livekit.RoomCompositeEgressRequest_Segments{
					Segments: &livekit.SegmentedFileOutput{
						FilenamePrefix: getFilePath(conf.ServiceConfig, test.filename),
						PlaylistName:   test.playlist,
						FilenameSuffix: test.filenameSuffix,
					},
				}
			}

			if test.options != nil {
				room.Options = &livekit.RoomCompositeEgressRequest_Advanced{
					Advanced: test.options,
				}
			}

			req := &livekit.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &livekit.StartEgressRequest_RoomComposite{
					RoomComposite: room,
				},
			}

			runSegmentsTest(t, conf, req, test)
		})
		if conf.Short {
			return
		}
	}
}

func testRoomCompositeMulti(t *testing.T, conf *TestConfig) {
	awaitIdle(t, conf.svc)

	req := &livekit.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &livekit.StartEgressRequest_RoomComposite{
			RoomComposite: &livekit.RoomCompositeEgressRequest{
				RoomName: conf.room.Name(),
				Layout:   "grid-light",
				FileOutputs: []*livekit.EncodedFileOutput{{
					FileType: livekit.EncodedFileType_MP4,
					Filepath: getFilePath(conf.ServiceConfig, "rc_multiple_{time}"),
				}},
				StreamOutputs: []*livekit.StreamOutput{{
					Protocol: livekit.StreamProtocol_RTMP,
					Urls:     []string{streamUrl1},
				}},
			},
		},
	}

	runMultipleTest(t, conf, req, true, true, false, livekit.SegmentedFileSuffix_TIMESTAMP)
}
