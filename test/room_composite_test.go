//go:build integration

package test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

func testRoomComposite(t *testing.T, conf *testConfig) {
	publishSamplesToRoom(t, conf.room, params.MimeTypeOpus, params.MimeTypeVP8, conf.Muting)

	now := time.Now().Unix()
	if !conf.StreamTestsOnly && !conf.SegmentedFileTestsOnly {
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
		} {
			t.Run(test.name, func(t *testing.T) {
				runRoomCompositeFileTest(t, conf, test)
			})
		}
	}

	if !conf.FileTestsOnly && !conf.SegmentedFileTestsOnly {
		// TODO: this kills the X server
		// t.Run("rtmp-failure", func(t *testing.T) {
		// 	testStreamFailure(t, conf)
		// })
		t.Run("room-rtmp", func(t *testing.T) {
			testRoomCompositeStream(t, conf)
		})
		// Give some time for the previous handler to finish and release the room handling lock
		time.Sleep(1 * time.Second)
	}

	if !conf.FileTestsOnly && !conf.StreamTestsOnly {
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
		} {
			t.Run(test.name, func(t *testing.T) {
				runRoomCompositeSegmentsTest(t, conf, test)
			})
		}
	}
}

func runRoomCompositeFileTest(t *testing.T, conf *testConfig, test *testCase) {
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
}

func testRoomCompositeStream(t *testing.T, conf *testConfig) {
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

	runStreamTest(t, conf, req)
}

func testStreamFailure(t *testing.T, conf *testConfig) {
	ctx := context.Background()

	req := &livekit.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &livekit.StartEgressRequest_RoomComposite{
			RoomComposite: &livekit.RoomCompositeEgressRequest{
				RoomName: conf.RoomName,
				Layout:   "speaker-light",
				Output: &livekit.RoomCompositeEgressRequest_Stream{
					Stream: &livekit.StreamOutput{
						Protocol: livekit.StreamProtocol_RTMP,
						Urls:     []string{badStreamUrl1},
					},
				},
			},
		},
	}

	p, err := params.GetPipelineParams(ctx, conf.Config, req)
	require.NoError(t, err)

	rec, err := pipeline.New(ctx, conf.Config, p)
	require.NoError(t, err)

	info := rec.Run(ctx)
	require.NotEmpty(t, info.Error)
	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, info.Status)
}

func runRoomCompositeSegmentsTest(t *testing.T, conf *testConfig, test *testCase) {
	webRequest := &livekit.RoomCompositeEgressRequest{
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
		webRequest.Options = &livekit.RoomCompositeEgressRequest_Advanced{
			Advanced: test.options,
		}
	}

	req := &livekit.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &livekit.StartEgressRequest_RoomComposite{
			RoomComposite: webRequest,
		},
	}

	runSegmentsTest(t, conf, req, getFilePath(conf.Config, test.playlist))
}
