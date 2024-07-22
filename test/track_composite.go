// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package test

import (
	"path"
	"testing"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

func (r *Runner) testTrackComposite(t *testing.T) {
	if !r.should(runTrackComposite) {
		return
	}

	r.sourceFramerate = 23.97
	r.testTrackCompositeFile(t)
	r.testTrackCompositeStream(t)
	r.testTrackCompositeSegments(t)
	r.testTrackCompositeImages(t)
	r.testTrackCompositeMulti(t)
}

func (r *Runner) runTrackTest(
	t *testing.T, name string, audioCodec, videoCodec types.MimeType,
	f func(t *testing.T, audioTrackID, videoTrackID string),
) {
	t.Run(name, func(t *testing.T) {
		r.awaitIdle(t)
		audioTrackID, videoTrackID := r.publishSamples(t, audioCodec, videoCodec)
		f(t, audioTrackID, videoTrackID)
	})
}

func (r *Runner) testTrackCompositeFile(t *testing.T) {
	if !r.should(runFile) {
		return
	}

	t.Run("4A/TrackComposite/File", func(t *testing.T) {
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
			r.runTrackTest(t, test.name, test.audioCodec, test.videoCodec, func(t *testing.T, audioTrackID, videoTrackID string) {
				var aID, vID string
				if !test.audioOnly {
					vID = videoTrackID
				}
				if !test.videoOnly {
					aID = audioTrackID
				}

				var fileOutput *livekit.EncodedFileOutput
				if r.AzureUpload != nil {
					fileOutput = &livekit.EncodedFileOutput{
						FileType: test.fileType,
						Filepath: path.Join(uploadPrefix, test.filename),
						Output: &livekit.EncodedFileOutput_Azure{
							Azure: r.AzureUpload,
						},
					}
				} else {
					fileOutput = &livekit.EncodedFileOutput{
						FileType: test.fileType,
						Filepath: path.Join(r.FilePrefix, test.filename),
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

				test.expectVideoEncoding = true
				r.runFileTest(t, req, test)
			})
			if r.Short {
				return
			}
		}
	})
}

func (r *Runner) testTrackCompositeStream(t *testing.T) {
	if !r.should(runStream) {
		return
	}

	r.runTrackTest(t, "4B/TrackComposite/Stream", types.MimeTypeOpus, types.MimeTypeVP8,
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

			r.runStreamTest(t, req, &testCase{expectVideoEncoding: true})
		},
	)
}

func (r *Runner) testTrackCompositeSegments(t *testing.T) {
	if !r.should(runSegments) {
		return
	}

	t.Run("4C/TrackComposite/Segments", func(t *testing.T) {
		for _, test := range []*testCase{
			{
				name:       "VP8",
				audioCodec: types.MimeTypeOpus,
				videoCodec: types.MimeTypeVP8,
				filename:   "tcs_{publisher_identity}_vp8_{time}",
				playlist:   "tcs_{publisher_identity}_vp8_{time}.m3u8",
			},
			{
				name:         "H264",
				audioCodec:   types.MimeTypeOpus,
				videoCodec:   types.MimeTypeH264,
				filename:     "tcs_{room_name}_h264_{time}",
				playlist:     "tcs_{room_name}_h264_{time}.m3u8",
				livePlaylist: "tcs_live_{room_name}_h264_{time}.m3u8",
			},
			{
				name:       "Audio Only",
				audioCodec: types.MimeTypeOpus,
				filename:   "tcs_{room_name}_audio_{time}",
				playlist:   "tcs_{room_name}_audio_{time}.m3u8",
				audioOnly:  true,
			},
		} {
			r.runTrackTest(t, test.name, test.audioCodec, test.videoCodec,
				func(t *testing.T, audioTrackID, videoTrackID string) {
					var aID, vID string
					if !test.audioOnly {
						vID = videoTrackID
					}
					if !test.videoOnly {
						aID = audioTrackID
					}

					var segmentOutput *livekit.SegmentedFileOutput
					if test.filenameSuffix == livekit.SegmentedFileSuffix_INDEX && r.S3Upload != nil {
						segmentOutput = &livekit.SegmentedFileOutput{
							FilenamePrefix:   path.Join(uploadPrefix, test.filename),
							PlaylistName:     test.playlist,
							LivePlaylistName: test.livePlaylist,
							FilenameSuffix:   test.filenameSuffix,
							Output: &livekit.SegmentedFileOutput_S3{
								S3: r.S3Upload,
							},
						}
					} else {
						segmentOutput = &livekit.SegmentedFileOutput{
							FilenamePrefix:   path.Join(r.FilePrefix, test.filename),
							PlaylistName:     test.playlist,
							LivePlaylistName: test.livePlaylist,
							FilenameSuffix:   test.filenameSuffix,
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
					test.expectVideoEncoding = true

					r.runSegmentsTest(t, req, test)
				},
			)
			if r.Short {
				return
			}
		}
	})
}

func (r *Runner) testTrackCompositeImages(t *testing.T) {
	if !r.should(runImages) {
		return
	}

	t.Run("4D/TrackComposite/Images", func(t *testing.T) {
		for _, test := range []*testCase{
			{
				name:       "H264",
				audioCodec: types.MimeTypeOpus,
				videoCodec: types.MimeTypeH264,
				filename:   "tc_{publisher_identity}_h264",
			},
		} {
			r.runTrackTest(t, test.name, test.audioCodec, test.videoCodec,
				func(t *testing.T, audioTrackID, videoTrackID string) {
					var aID, vID string
					if !test.audioOnly {
						vID = videoTrackID
					}
					if !test.videoOnly {
						aID = audioTrackID
					}

					var imageOutput *livekit.ImageOutput
					if r.S3Upload != nil {
						imageOutput = &livekit.ImageOutput{
							CaptureInterval: 5,
							Width:           1280,
							Height:          720,
							FilenamePrefix:  path.Join(uploadPrefix, test.filename),
							Output: &livekit.ImageOutput_S3{
								S3: r.S3Upload,
							},
						}
					} else {
						imageOutput = &livekit.ImageOutput{
							CaptureInterval: 5,
							Width:           1280,
							Height:          720,
							FilenamePrefix:  path.Join(r.FilePrefix, test.filename),
						}
					}

					trackRequest := &livekit.TrackCompositeEgressRequest{
						RoomName:     r.room.Name(),
						AudioTrackId: aID,
						VideoTrackId: vID,
						ImageOutputs: []*livekit.ImageOutput{imageOutput},
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
					r.runImagesTest(t, req, test)
				},
			)
			if r.Short {
				return
			}
		}
	})
}

func (r *Runner) testTrackCompositeMulti(t *testing.T) {
	if !r.should(runMulti) {
		return
	}

	r.runTrackTest(t, "4E/TrackComposite/Multi", types.MimeTypeOpus, types.MimeTypeVP8,
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
							FilenamePrefix: path.Join(r.FilePrefix, "tc_multiple_{time}"),
							PlaylistName:   "tc_multiple_{time}",
						}},
					},
				},
			}

			r.runMultipleTest(t, req, false, true, true, false, livekit.SegmentedFileSuffix_INDEX)
		},
	)
}
