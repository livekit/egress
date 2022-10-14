//go:build integration

package test

import (
	"testing"
	"time"

	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

func testTrackCompositeFile(t *testing.T, conf *TestConfig) {
	for _, test := range []*testCase{
		{
			name:       "tc-vp8-mp4",
			fileType:   livekit.EncodedFileType_MP4,
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeVP8,
			filename:   "tc_{publisher_identity}_vp8_{time}.mp4",
		},
		{
			name:       "tc-h264-mp4",
			fileType:   livekit.EncodedFileType_MP4,
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeH264,
			filename:   "tc_{room_name}_h264_{time}.mp4",
		},
		{
			name:           "tc-limit",
			fileType:       livekit.EncodedFileType_MP4,
			audioCodec:     params.MimeTypeOpus,
			videoCodec:     params.MimeTypeH264,
			filename:       "tc_limit_{time}.mp4",
			sessionTimeout: time.Second * 20,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)
			audioTrackID, videoTrackID := publishSamplesToRoom(t, conf.room, test.audioCodec, test.videoCodec, conf.Muting)

			trackRequest := &livekit.TrackCompositeEgressRequest{
				RoomName: conf.room.Name(),
				Output: &livekit.TrackCompositeEgressRequest_File{
					File: &livekit.EncodedFileOutput{
						FileType: test.fileType,
						Filepath: getFilePath(conf.Config, test.filename),
					},
				},
			}
			if !test.audioOnly {
				trackRequest.VideoTrackId = videoTrackID
			}
			if !test.videoOnly {
				trackRequest.AudioTrackId = audioTrackID
			}

			if test.options != nil {
				trackRequest.Options = &livekit.TrackCompositeEgressRequest_Advanced{
					Advanced: test.options,
				}
			}

			req := &livekit.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &livekit.StartEgressRequest_TrackComposite{
					TrackComposite: trackRequest,
				},
			}

			runFileTest(t, conf, req, test)
		})
	}
}

func testTrackCompositeStream(t *testing.T, conf *TestConfig) {
	for _, test := range []*testCase{
		{
			name: "tc-rtmp",
		},
		{
			name:           "tc-rtmp-limit",
			sessionTimeout: time.Second * 20,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)
			audioTrackID, videoTrackID := publishSamplesToRoom(t, conf.room, params.MimeTypeOpus, params.MimeTypeVP8, conf.Muting)

			req := &livekit.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &livekit.StartEgressRequest_TrackComposite{
					TrackComposite: &livekit.TrackCompositeEgressRequest{
						RoomName:     conf.room.Name(),
						AudioTrackId: audioTrackID,
						VideoTrackId: videoTrackID,
						Output: &livekit.TrackCompositeEgressRequest_Stream{
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

func testTrackCompositeSegments(t *testing.T, conf *TestConfig) {
	for _, test := range []*testCase{
		{
			name:       "tcs-vp8",
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeVP8,
			filename:   "tcs_{publisher_identity}_vp8_{time}",
			playlist:   "tcs_{publisher_identity}_vp8_{time}.m3u8",
		},
		{
			name:       "tcs-h264",
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeH264,
			filename:   "tcs_{room_name}_h264_{time}",
			playlist:   "tcs_{room_name}_h264_{time}.m3u8",
		},
		{
			name:       "tcs-limit",
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeH264,
			filename:   "tcs-limit-{time}",
			playlist:   "tcs-limit-{time}.m3u8",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)
			audioTrackID, videoTrackID := publishSamplesToRoom(t, conf.room, test.audioCodec, test.videoCodec, conf.Muting)

			var aID, vID string
			if !test.audioOnly {
				vID = videoTrackID
			}
			if !test.videoOnly {
				aID = audioTrackID
			}

			filepath := getFilePath(conf.Config, test.filename)
			trackRequest := &livekit.TrackCompositeEgressRequest{
				RoomName:     conf.room.Name(),
				AudioTrackId: aID,
				VideoTrackId: vID,
				Output: &livekit.TrackCompositeEgressRequest_Segments{
					Segments: &livekit.SegmentedFileOutput{
						FilenamePrefix: filepath,
						PlaylistName:   test.playlist,
					},
				},
			}

			if test.options != nil {
				trackRequest.Options = &livekit.TrackCompositeEgressRequest_Advanced{
					Advanced: test.options,
				}
			}

			req := &livekit.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &livekit.StartEgressRequest_TrackComposite{
					TrackComposite: trackRequest,
				},
			}

			runSegmentsTest(t, conf, req, test.sessionTimeout)
		})
	}
}
