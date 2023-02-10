//go:build integration

package test

import (
	"testing"
	"time"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

func testTrackCompositeFile(t *testing.T, conf *TestConfig) {
	for _, test := range []*testCase{
		{
			name:       "tc-vp8-mp4",
			fileType:   livekit.EncodedFileType_MP4,
			audioCodec: types.MimeTypeOpus,
			videoCodec: types.MimeTypeVP8,
			filename:   "tc_{publisher_identity}_vp8_{time}.mp4",
		},
		{
			name:       "tc-h264-mp4",
			fileType:   livekit.EncodedFileType_MP4,
			audioCodec: types.MimeTypeOpus,
			videoCodec: types.MimeTypeH264,
			filename:   "tc_{room_name}_h264_{time}.mp4",
		},
		{
			name:           "tc-limit",
			fileType:       livekit.EncodedFileType_MP4,
			audioCodec:     types.MimeTypeOpus,
			videoCodec:     types.MimeTypeH264,
			filename:       "tc_limit_{time}.mp4",
			sessionTimeout: time.Second * 20,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)
			audioTrackID, videoTrackID := publishSamplesToRoom(t, conf.room, test.audioCodec, test.videoCodec, conf.Muting)

			fileOutput := &livekit.EncodedFileOutput{
				FileType: test.fileType,
				Filepath: getFilePath(conf.ServiceConfig, test.filename),
			}

			if conf.AzureUpload != nil {
				fileOutput.Filepath = test.filename
				fileOutput.Output = &livekit.EncodedFileOutput_Azure{
					Azure: conf.AzureUpload,
				}
			}

			trackRequest := &livekit.TrackCompositeEgressRequest{
				RoomName: conf.room.Name(),
			}
			if conf.V2 {
				trackRequest.FileOutputs = []*livekit.EncodedFileOutput{fileOutput}
			} else {
				trackRequest.Output = &livekit.TrackCompositeEgressRequest_File{
					File: fileOutput,
				}
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

			test.expectVideoTranscoding = true
			runFileTest(t, conf, req, test)
		})
		if conf.Short {
			return
		}
	}
}

func testTrackCompositeStream(t *testing.T, conf *TestConfig) {
	for _, test := range []*testCase{
		{
			name:                   "tc-rtmp",
			expectVideoTranscoding: true,
		},
		{
			name:                   "tc-rtmp-limit",
			sessionTimeout:         time.Second * 20,
			expectVideoTranscoding: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			awaitIdle(t, conf.svc)
			audioTrackID, videoTrackID := publishSamplesToRoom(t, conf.room, types.MimeTypeOpus, types.MimeTypeVP8, conf.Muting)

			trackRequest := &livekit.TrackCompositeEgressRequest{
				RoomName:     conf.room.Name(),
				AudioTrackId: audioTrackID,
				VideoTrackId: videoTrackID,
			}
			if conf.V2 {
				trackRequest.StreamOutputs = []*livekit.StreamOutput{{
					Urls: []string{streamUrl1},
				}}
			} else {
				trackRequest.Output = &livekit.TrackCompositeEgressRequest_Stream{
					Stream: &livekit.StreamOutput{
						Urls: []string{streamUrl1},
					},
				}
			}

			req := &livekit.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &livekit.StartEgressRequest_TrackComposite{
					TrackComposite: trackRequest,
				},
			}
			runStreamTest(t, conf, req, test)
		})
		if conf.Short {
			return
		}
	}
}

func testTrackCompositeSegments(t *testing.T, conf *TestConfig) {
	for _, test := range []*testCase{
		{
			name:       "tcs-vp8",
			audioCodec: types.MimeTypeOpus,
			videoCodec: types.MimeTypeVP8,
			filename:   "tcs_{publisher_identity}_vp8_{time}",
			playlist:   "tcs_{publisher_identity}_vp8_{time}.m3u8",
		},
		{
			name:       "tcs-h264",
			audioCodec: types.MimeTypeOpus,
			videoCodec: types.MimeTypeH264,
			filename:   "tcs_{room_name}_h264_{time}",
			playlist:   "tcs_{room_name}_h264_{time}.m3u8",
		},
		{
			name:       "tcs-limit",
			audioCodec: types.MimeTypeOpus,
			videoCodec: types.MimeTypeH264,
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

			filepath := getFilePath(conf.ServiceConfig, test.filename)
			trackRequest := &livekit.TrackCompositeEgressRequest{
				RoomName:     conf.room.Name(),
				AudioTrackId: aID,
				VideoTrackId: vID,
			}
			if conf.V2 {
				trackRequest.SegmentOutputs = []*livekit.SegmentedFileOutput{{
					FilenamePrefix: filepath,
					PlaylistName:   test.playlist,
				}}
			} else {
				trackRequest.Output = &livekit.TrackCompositeEgressRequest_Segments{
					Segments: &livekit.SegmentedFileOutput{
						FilenamePrefix: filepath,
						PlaylistName:   test.playlist,
					},
				}
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
			test.expectVideoTranscoding = true
			runSegmentsTest(t, conf, req, test)
		})
		if conf.Short {
			return
		}
	}
}
