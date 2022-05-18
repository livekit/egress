//go:build integration
// +build integration

package test

import (
	"fmt"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go"

	"github.com/livekit/egress/pkg/pipeline/params"
)

func testRoomComposite(t *testing.T, conf *testConfig, room *lksdk.Room) {
	if room != nil {
		audioTrackID := publishSampleToRoom(t, room, params.MimeTypeOpus, conf.Muting)
		t.Cleanup(func() {
			_ = room.LocalParticipant.UnpublishTrack(audioTrackID)
		})

		videoTrackID := publishSampleToRoom(t, room, params.MimeTypeVP8, conf.Muting)
		t.Cleanup(func() {
			_ = room.LocalParticipant.UnpublishTrack(videoTrackID)
		})
	}

	if conf.RunFileTests {
		for _, test := range []*testCase{
			{
				name:     "h264-high-mp4",
				inputUrl: videoTestInput,
				fileType: livekit.EncodedFileType_MP4,
				options: &livekit.EncodingOptions{
					AudioCodec:   livekit.AudioCodec_AAC,
					VideoCodec:   livekit.VideoCodec_H264_HIGH,
					Height:       720,
					Width:        1280,
					VideoBitrate: 4500,
				},
				filename: fmt.Sprintf("room-h264-high-%v.mp4", time.Now().Unix()),
			},
			{
				name:             "h264-baseline-mp4",
				inputUrl:         staticTestInput,
				forceCustomInput: true,
				fileType:         livekit.EncodedFileType_MP4,
				options: &livekit.EncodingOptions{
					AudioCodec:   livekit.AudioCodec_AAC,
					VideoCodec:   livekit.VideoCodec_H264_BASELINE,
					Height:       720,
					Width:        1280,
					VideoBitrate: 1500,
				},
				filename: fmt.Sprintf("room-h264-baseline-%v.mp4", time.Now().Unix()),
			},
		} {
			if !t.Run(test.name, func(t *testing.T) {
				runRoomCompositeFileTest(t, conf, test)
			}) {
				t.FailNow()
			}
		}

		// TODO: get rid of this error, probably by calling Ref() on something
		//  (test.test:9038): GStreamer-CRITICAL **: 23:46:45.257:
		//  gst_mini_object_unref: assertion 'GST_MINI_OBJECT_REFCOUNT_VALUE (mini_object) > 0' failed
		if !t.Run("room-opus-ogg-simultaneous", func(t *testing.T) {
			finished := make(chan struct{})
			go func() {
				runRoomCompositeFileTest(t, conf, &testCase{
					inputUrl:         audioTestInput,
					forceCustomInput: true,
					fileType:         livekit.EncodedFileType_OGG,
					audioOnly:        true,
					options: &livekit.EncodingOptions{
						AudioCodec: livekit.AudioCodec_OPUS,
					},
					filename: fmt.Sprintf("room-opus-1-%v.ogg", time.Now().Unix()),
				})
				close(finished)
			}()

			runRoomCompositeFileTest(t, conf, &testCase{
				inputUrl:         audioTestInput2,
				forceCustomInput: true,
				fileType:         livekit.EncodedFileType_OGG,
				audioOnly:        true,
				options: &livekit.EncodingOptions{
					AudioCodec: livekit.AudioCodec_OPUS,
				},
				filename: fmt.Sprintf("room-opus-2-%v.ogg", time.Now().Unix()),
			})

			<-finished
		}) {
			t.FailNow()
		}
	}

	if conf.RunStreamTests {
		if !t.Run("room-rtmp", func(t *testing.T) {
			testRoomCompositeStream(t, conf)
		}) {
			t.FailNow()
		}
	}
}

func runRoomCompositeFileTest(t *testing.T, conf *testConfig, test *testCase) {
	filepath := getFilePath(conf.Config, test.filename)
	webRequest := &livekit.RoomCompositeEgressRequest{
		RoomName:  conf.RoomName,
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
		webRequest.Options = &livekit.RoomCompositeEgressRequest_Advanced{
			Advanced: test.options,
		}
	}

	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().UnixNano(),
		Request: &livekit.StartEgressRequest_RoomComposite{
			RoomComposite: webRequest,
		},
	}

	runFileTest(t, conf, test, req, filepath)
}

func testRoomCompositeStream(t *testing.T, conf *testConfig) {
	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().Unix(),
		Request: &livekit.StartEgressRequest_RoomComposite{
			RoomComposite: &livekit.RoomCompositeEgressRequest{
				RoomName:      conf.RoomName,
				Layout:        "speaker-dark",
				CustomBaseUrl: videoTestInput,
				Output: &livekit.RoomCompositeEgressRequest_Stream{
					Stream: &livekit.StreamOutput{
						Protocol: livekit.StreamProtocol_RTMP,
						Urls:     []string{streamUrl1},
					},
				},
				Options: &livekit.RoomCompositeEgressRequest_Advanced{
					Advanced: &livekit.EncodingOptions{
						AudioCodec: livekit.AudioCodec_AAC,
					},
				},
			},
		},
	}

	runStreamTest(t, conf, req)
}
