//go:build integration

package test

import (
	"fmt"
	"testing"
	"time"

	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

func testTrackCompositeFile(t *testing.T, conf *Config) {
	now := time.Now().Unix()
	for _, test := range []*testCase{
		{
			name:       "tc-vp8-mp4",
			fileType:   livekit.EncodedFileType_MP4,
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeVP8,
			filename:   fmt.Sprintf("tc-vp8-%v.mp4", now),
		},
		{
			name:       "tc-h264-mp4",
			fileType:   livekit.EncodedFileType_MP4,
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeH264,
			filename:   fmt.Sprintf("tc-h264-%v.mp4", now),
		},
		{
			name:           "tc-h264-mp4-timedout",
			fileType:       livekit.EncodedFileType_MP4,
			audioCodec:     params.MimeTypeOpus,
			videoCodec:     params.MimeTypeH264,
			filename:       fmt.Sprintf("tc-h264-%v.mp4", now),
			sessionTimeout: time.Second * 20,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)
			audioTrackID, videoTrackID := publishSamplesToRoom(t, conf.room, test.audioCodec, test.videoCodec, conf.Muting)

			filepath := getFilePath(conf.Config, test.filename)
			trackRequest := &livekit.TrackCompositeEgressRequest{
				RoomName: conf.room.Name(),
				Output: &livekit.TrackCompositeEgressRequest_File{
					File: &livekit.EncodedFileOutput{
						FileType: test.fileType,
						Filepath: filepath,
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

			runFileTest(t, conf, req, test, filepath)
		})
	}
}

func testTrackCompositeStream(t *testing.T, conf *Config) {
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

func testTrackCompositeSegments(t *testing.T, conf *Config) {
	now := time.Now().Unix()
	for _, test := range []*testCase{
		{
			name:       "tc-vp8-hls",
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeVP8,
			filename:   fmt.Sprintf("tc-vp8-hls-%v", now),
			playlist:   fmt.Sprintf("tc-vp8-hls-%v.m3u8", now),
		},
		{
			name:       "tc-h264-hls",
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeH264,
			filename:   fmt.Sprintf("tc-h264-hls-%v", now),
			playlist:   fmt.Sprintf("tc-h264-hls-%v.m3u8", now),
		}, {
			name:       "tc-h264-hls-timedout",
			audioCodec: params.MimeTypeOpus,
			videoCodec: params.MimeTypeH264,
			filename:   fmt.Sprintf("tc-h264-hls-timedout-%v", now),
			playlist:   fmt.Sprintf("tc-h264-hls-timedout-%v.m3u8", now),
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

			runSegmentsTest(t, conf, req, getFilePath(conf.Config, test.playlist), test.sessionTimeout)
		})
	}
}
