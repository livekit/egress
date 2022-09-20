package test

import (
	"fmt"
	"testing"
	"time"

	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

func testParticipantCompositeFile(t *testing.T, conf *Context) {
	now := time.Now().Unix()
	for _, test := range []*testCase{
		{
			name:       "pc-vp8-mp4",
			fileType:   livekit.EncodedFileType_MP4,
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeVP8,
			filename:   fmt.Sprintf("pc-vp8-%v.mp4", now),
		},
		{
			name:       "pc-h264-mp4",
			fileType:   livekit.EncodedFileType_MP4,
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeH264,
			filename:   fmt.Sprintf("pc-h264-%v.mp4", now),
		},
		{
			name:           "pc-h264-mp4-limit",
			fileType:       livekit.EncodedFileType_MP4,
			audioCodec:     params.MimeTypeOpus,
			videoCodec:     params.MimeTypeH264,
			filename:       fmt.Sprintf("pc-h264-%v.mp4", now),
			sessionTimeout: time.Second * 20,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)
			publishSamplesToRoom(t, conf.room, test.audioCodec, test.videoCodec, conf.Muting)

			filepath := getFilePath(conf.Config, test.filename)
			participantRequest := &livekit.ParticipantCompositeEgressRequest{
				RoomName:            conf.room.Name(),
				ParticipantIdentity: conf.room.LocalParticipant.Identity(),
				Output: &livekit.ParticipantCompositeEgressRequest_File{
					File: &livekit.EncodedFileOutput{
						FileType: test.fileType,
						Filepath: filepath,
					},
				},
			}

			if test.options != nil {
				participantRequest.Options = &livekit.ParticipantCompositeEgressRequest_Advanced{
					Advanced: test.options,
				}
			}

			req := &livekit.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &livekit.StartEgressRequest_ParticipantComposite{
					ParticipantComposite: participantRequest,
				},
			}

			runFileTest(t, conf, req, test, filepath)
		})
	}
}

func testParticipantCompositeStream(t *testing.T, conf *Context) {
	for _, test := range []*testCase{
		{
			name: "pc-rtmp",
		},
		{
			name:           "pc-rtmp-limit",
			sessionTimeout: time.Second * 20,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)
			publishSamplesToRoom(t, conf.room, params.MimeTypeOpus, params.MimeTypeVP8, conf.Muting)

			req := &livekit.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &livekit.StartEgressRequest_ParticipantComposite{
					ParticipantComposite: &livekit.ParticipantCompositeEgressRequest{
						RoomName:            conf.room.Name(),
						ParticipantIdentity: conf.room.LocalParticipant.Identity(),
						Output: &livekit.ParticipantCompositeEgressRequest_Stream{
							Stream: &livekit.StreamOutput{
								Urls: []string{streamUrl1},
							},
						},
					},
				},
			}

			runStreamTest(t, conf, req, test.sessionTimeout)
		})
	}
}

func testParticipantCompositeSegments(t *testing.T, conf *Context) {
	now := time.Now().Unix()
	for _, test := range []*testCase{
		{
			name:       "pc-vp8-hls",
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeVP8,
			filename:   fmt.Sprintf("pc-vp8-hls-%v", now),
			playlist:   fmt.Sprintf("pc-vp8-hls-%v.m3u8", now),
		},
		{
			name:       "pc-h264-hls",
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeH264,
			filename:   fmt.Sprintf("pc-h264-hls-%v", now),
			playlist:   fmt.Sprintf("pc-h264-hls-%v.m3u8", now),
		}, {
			name:       "pc-h264-hls-limit",
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeH264,
			filename:   fmt.Sprintf("pc-h264-hls-limit-%v", now),
			playlist:   fmt.Sprintf("pc-h264-hls-limit-%v.m3u8", now),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)
			publishSamplesToRoom(t, conf.room, test.audioCodec, test.videoCodec, conf.Muting)

			filepath := getFilePath(conf.Config, test.filename)
			participantRequest := &livekit.ParticipantCompositeEgressRequest{
				RoomName:            conf.room.Name(),
				ParticipantIdentity: conf.room.LocalParticipant.Identity(),
				Output: &livekit.ParticipantCompositeEgressRequest_Segments{
					Segments: &livekit.SegmentedFileOutput{
						FilenamePrefix: filepath,
						PlaylistName:   test.playlist,
					},
				},
			}

			if test.options != nil {
				participantRequest.Options = &livekit.ParticipantCompositeEgressRequest_Advanced{
					Advanced: test.options,
				}
			}

			req := &livekit.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &livekit.StartEgressRequest_ParticipantComposite{
					ParticipantComposite: participantRequest,
				},
			}

			runSegmentsTest(t, conf, req, getFilePath(conf.Config, test.playlist), test.sessionTimeout)
		})
	}
}
