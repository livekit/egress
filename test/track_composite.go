//go:build integration

package test

import (
	"testing"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

func (r *Runner) testTrackComposite(t *testing.T) {
	if !r.runTrackCompositeTests() {
		return
	}

	r.sourceFramerate = 23.97
	r.testTrackCompositeFile(t)
	r.testTrackCompositeStream(t)
	r.testTrackCompositeSegments(t)
	r.testTrackCompositeMulti(t)
}

func (r *Runner) runSDKTest(t *testing.T, name string, audioCodec, videoCodec types.MimeType,
	f func(t *testing.T, audioTrackID, videoTrackID string),
) {
	t.Run(name, func(t *testing.T) {
		r.awaitIdle(t)
		audioTrackID, videoTrackID := r.publishSamplesToRoom(t, audioCodec, videoCodec)
		f(t, audioTrackID, videoTrackID)
		if t.Failed() {
			r.svc.Reset()
			go r.svc.Run()
		}
	})
}

func (r *Runner) testTrackCompositeFile(t *testing.T) {
	if !r.runFileTests() {
		return
	}

	t.Run("TrackComposite/File", func(t *testing.T) {
		for _, test := range []*testCase{
			{
				name:       "VP8",
				fileType:   livekit.EncodedFileType_MP4,
				audioCodec: types.MimeTypeOpus,
				videoCodec: types.MimeTypeVP8,
				filename:   "tc_{publisher_identity}_vp8_{time}.mp4",
			},
			{
				name:       "H264",
				fileType:   livekit.EncodedFileType_MP4,
				audioCodec: types.MimeTypeOpus,
				videoCodec: types.MimeTypeH264,
				filename:   "tc_{room_name}_h264_{time}.mp4",
			},
		} {
			r.runSDKTest(t, test.name, test.audioCodec, test.videoCodec, func(t *testing.T, audioTrackID, videoTrackID string) {
				var aID, vID string
				if !test.audioOnly {
					vID = videoTrackID
				}
				if !test.videoOnly {
					aID = audioTrackID
				}

				fileOutput := &livekit.EncodedFileOutput{
					FileType: test.fileType,
					Filepath: r.getFilePath(test.filename),
				}
				if test.filenameSuffix == livekit.SegmentedFileSuffix_INDEX && r.AzureUpload != nil {
					fileOutput.Filepath = test.filename
					fileOutput.Output = &livekit.EncodedFileOutput_Azure{
						Azure: r.AzureUpload,
					}
				}

				trackRequest := &livekit.TrackCompositeEgressRequest{
					RoomName:     r.room.Name(),
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
				r.runFileTest(t, req, test)
			})
			if r.Short {
				return
			}
		}
	})
}

func (r *Runner) testTrackCompositeStream(t *testing.T) {
	if !r.runStreamTests() {
		return
	}

	r.runSDKTest(t, "TrackComposite/Stream", types.MimeTypeOpus, types.MimeTypeVP8,
		func(t *testing.T, audioTrackID, videoTrackID string) {
			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_TrackComposite{
					TrackComposite: &livekit.TrackCompositeEgressRequest{
						RoomName:     r.room.Name(),
						AudioTrackId: audioTrackID,
						VideoTrackId: videoTrackID,
						StreamOutputs: []*livekit.StreamOutput{{
							Urls: []string{streamUrl1, badStreamUrl1},
						}},
					},
				},
			}

			r.runStreamTest(t, req, &testCase{expectVideoTranscoding: true})
		},
	)
}

func (r *Runner) testTrackCompositeSegments(t *testing.T) {
	if !r.runSegmentTests() {
		return
	}

	t.Run("TrackComposite/Segments", func(t *testing.T) {
		for _, test := range []*testCase{
			{
				name:       "VP8",
				audioCodec: types.MimeTypeOpus,
				videoCodec: types.MimeTypeVP8,
				filename:   "tcs_{publisher_identity}_vp8_{time}",
				playlist:   "tcs_{publisher_identity}_vp8_{time}.m3u8",
			},
			{
				name:       "H264",
				audioCodec: types.MimeTypeOpus,
				videoCodec: types.MimeTypeH264,
				filename:   "tcs_{room_name}_h264_{time}",
				playlist:   "tcs_{room_name}_h264_{time}.m3u8",
			},
		} {
			r.runSDKTest(t, test.name, test.audioCodec, test.videoCodec,
				func(t *testing.T, audioTrackID, videoTrackID string) {
					var aID, vID string
					if !test.audioOnly {
						vID = videoTrackID
					}
					if !test.videoOnly {
						aID = audioTrackID
					}

					segmentOutput := &livekit.SegmentedFileOutput{
						FilenamePrefix: r.getFilePath(test.filename),
						PlaylistName:   test.playlist,
						FilenameSuffix: test.filenameSuffix,
					}
					if test.filenameSuffix == livekit.SegmentedFileSuffix_INDEX && r.S3Upload != nil {
						segmentOutput.FilenamePrefix = test.filename
						segmentOutput.Output = &livekit.SegmentedFileOutput_S3{
							S3: r.S3Upload,
						}
					}

					trackRequest := &livekit.TrackCompositeEgressRequest{
						RoomName:       r.room.Name(),
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

					r.runSegmentsTest(t, req, test)
				},
			)
			if r.Short {
				return
			}
		}
	})
}

func (r *Runner) testTrackCompositeMulti(t *testing.T) {
	if !r.runMultiTests() {
		return
	}

	r.runSDKTest(t, "TrackComposite/Multi", types.MimeTypeOpus, types.MimeTypeVP8,
		func(t *testing.T, audioTrackID, videoTrackID string) {
			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_TrackComposite{
					TrackComposite: &livekit.TrackCompositeEgressRequest{
						RoomName:     r.room.Name(),
						AudioTrackId: audioTrackID,
						VideoTrackId: videoTrackID,
						StreamOutputs: []*livekit.StreamOutput{{
							Protocol: livekit.StreamProtocol_RTMP,
						}},
						SegmentOutputs: []*livekit.SegmentedFileOutput{{
							FilenamePrefix: r.getFilePath("tc_multiple_{time}"),
							PlaylistName:   "tc_multiple_{time}",
						}},
					},
				},
			}

			r.runMultipleTest(t, req, false, true, true, livekit.SegmentedFileSuffix_INDEX)
		},
	)
}
