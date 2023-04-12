//go:build integration

package test

import (
	"testing"
	"time"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

func runTrackCompositeTests(t *testing.T, conf *TestConfig) {
	if !conf.runTrackCompositeTests {
		return
	}

	conf.sourceFramerate = 23.97

	if conf.runFileTests {
		t.Run("TrackComposite/File", func(t *testing.T) {
			testTrackCompositeFile(t, conf)
		})
	}

	if conf.runStreamTests {
		t.Run("TrackComposite/Stream", func(t *testing.T) {
			testTrackCompositeStream(t, conf)
		})
	}

	if conf.runSegmentTests {
		t.Run("TrackComposite/Segments", func(t *testing.T) {
			testTrackCompositeSegments(t, conf)
		})
	}

	if conf.runMultiTests {
		runSDKTest(t, conf, "TrackComposite/Multi", types.MimeTypeOpus, types.MimeTypeVP8,
			func(t *testing.T, audioTrackID, videoTrackID string) {
				testTrackCompositeMulti(t, conf, audioTrackID, videoTrackID)
			},
		)
	}
}

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
		runSDKTest(t, conf, test.name, test.audioCodec, test.videoCodec, func(t *testing.T, audioTrackID, videoTrackID string) {
			var aID, vID string
			if !test.audioOnly {
				vID = videoTrackID
			}
			if !test.videoOnly {
				aID = audioTrackID
			}

			fileOutput := &livekit.EncodedFileOutput{
				FileType: test.fileType,
				Filepath: getFilePath(conf.ServiceConfig, test.filename),
			}
			if test.filenameSuffix == livekit.SegmentedFileSuffix_INDEX && conf.AzureUpload != nil {
				fileOutput.Filepath = test.filename
				fileOutput.Output = &livekit.EncodedFileOutput_Azure{
					Azure: conf.AzureUpload,
				}
			}

			trackRequest := &livekit.TrackCompositeEgressRequest{
				RoomName:     conf.room.Name(),
				AudioTrackId: aID,
				VideoTrackId: vID,
				FileOutputs:  []*livekit.EncodedFileOutput{fileOutput},
			}
			if test.options != nil {
				trackRequest.Options = &livekit.TrackCompositeEgressRequest_Advanced{
					Advanced: test.options,
				}
			}

			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_TrackComposite{
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
		runSDKTest(t, conf, test.name, types.MimeTypeOpus, types.MimeTypeVP8, func(t *testing.T, audioTrackID, videoTrackID string) {
			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_TrackComposite{
					TrackComposite: &livekit.TrackCompositeEgressRequest{
						RoomName:     conf.room.Name(),
						AudioTrackId: audioTrackID,
						VideoTrackId: videoTrackID,
						StreamOutputs: []*livekit.StreamOutput{{
							Urls: []string{streamUrl1},
						}},
					},
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
			name:           "tcs-limit",
			audioCodec:     types.MimeTypeOpus,
			videoCodec:     types.MimeTypeH264,
			filename:       "tcs_limit_{time}",
			playlist:       "tcs_limit_{time}.m3u8",
			filenameSuffix: livekit.SegmentedFileSuffix_TIMESTAMP,
		},
	} {
		runSDKTest(t, conf, test.name, test.audioCodec, test.videoCodec, func(t *testing.T, audioTrackID, videoTrackID string) {
			var aID, vID string
			if !test.audioOnly {
				vID = videoTrackID
			}
			if !test.videoOnly {
				aID = audioTrackID
			}

			segmentOutput := &livekit.SegmentedFileOutput{
				FilenamePrefix: getFilePath(conf.ServiceConfig, test.filename),
				PlaylistName:   test.playlist,
				FilenameSuffix: test.filenameSuffix,
			}
			if test.filenameSuffix == livekit.SegmentedFileSuffix_INDEX && conf.S3Upload != nil {
				segmentOutput.FilenamePrefix = test.filename
				segmentOutput.Output = &livekit.SegmentedFileOutput_S3{
					S3: conf.S3Upload,
				}
			}

			trackRequest := &livekit.TrackCompositeEgressRequest{
				RoomName:       conf.room.Name(),
				AudioTrackId:   aID,
				VideoTrackId:   vID,
				SegmentOutputs: []*livekit.SegmentedFileOutput{segmentOutput},
			}
			if test.options != nil {
				trackRequest.Options = &livekit.TrackCompositeEgressRequest_Advanced{
					Advanced: test.options,
				}
			}

			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_TrackComposite{
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

func testTrackCompositeMulti(t *testing.T, conf *TestConfig, audioTrackID, videoTrackID string) {
	req := &rpc.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &rpc.StartEgressRequest_TrackComposite{
			TrackComposite: &livekit.TrackCompositeEgressRequest{
				RoomName:     conf.room.Name(),
				AudioTrackId: audioTrackID,
				VideoTrackId: videoTrackID,
				StreamOutputs: []*livekit.StreamOutput{{
					Protocol: livekit.StreamProtocol_RTMP,
				}},
				SegmentOutputs: []*livekit.SegmentedFileOutput{{
					FilenamePrefix: getFilePath(conf.ServiceConfig, "tc_multiple_{time}"),
					PlaylistName:   "tc_multiple_{time}",
				}},
			},
		},
	}

	runMultipleTest(t, conf, req, false, true, true, livekit.SegmentedFileSuffix_INDEX)
}
