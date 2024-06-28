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

func (r *Runner) testRoomComposite(t *testing.T) {
	if !r.should(runRoom) {
		return
	}

	r.sourceFramerate = 30
	r.testRoomCompositeFile(t)
	r.testRoomCompositeStream(t)
	r.testRoomCompositeSegments(t)
	r.testRoomCompositeImages(t)
	r.testRoomCompositeMulti(t)
}

func (r *Runner) runRoomTest(t *testing.T, name string, audioCodec, videoCodec types.MimeType, f func(t *testing.T)) {
	t.Run(name, func(t *testing.T) {
		r.awaitIdle(t)
		r.publishSamples(t, audioCodec, videoCodec)
		f(t)
	})
}

func (r *Runner) testRoomCompositeFile(t *testing.T) {
	if !r.should(runFile) {
		return
	}

	t.Run("1A/RoomComposite/File", func(t *testing.T) {
		for _, test := range []*testCase{
			{
				name:                "Base",
				filename:            "r_{room_name}_{time}.mp4",
				expectVideoEncoding: true,
			},
			{
				name:      "Video-Only",
				videoOnly: true,
				options: &livekit.EncodingOptions{
					VideoCodec: livekit.VideoCodec_H264_HIGH,
				},
				filename:            "r_{room_name}_video_{time}.mp4",
				expectVideoEncoding: true,
			},
			{
				name:      "Audio-Only",
				fileType:  livekit.EncodedFileType_OGG,
				audioOnly: true,
				options: &livekit.EncodingOptions{
					AudioCodec: livekit.AudioCodec_OPUS,
				},
				filename:            "r_{room_name}_audio_{time}",
				expectVideoEncoding: false,
			},
		} {
			r.runRoomTest(t, test.name, types.MimeTypeOpus, types.MimeTypeH264, func(t *testing.T) {
				var fileOutput *livekit.EncodedFileOutput
				if r.S3Upload != nil {
					fileOutput = &livekit.EncodedFileOutput{
						FileType: test.fileType,
						Filepath: path.Join(uploadPrefix, test.filename),
						Output: &livekit.EncodedFileOutput_S3{
							S3: r.S3Upload,
						},
					}
				} else {
					fileOutput = &livekit.EncodedFileOutput{
						FileType: test.fileType,
						Filepath: path.Join(r.FilePrefix, test.filename),
					}
				}

				roomRequest := &livekit.RoomCompositeEgressRequest{
					RoomName:    r.room.Name(),
					Layout:      "speaker-dark",
					AudioOnly:   test.audioOnly,
					VideoOnly:   test.videoOnly,
					FileOutputs: []*livekit.EncodedFileOutput{fileOutput},
				}
				if test.options != nil {
					roomRequest.Options = &livekit.RoomCompositeEgressRequest_Advanced{
						Advanced: test.options,
					}
				} else if test.preset != 0 {
					roomRequest.Options = &livekit.RoomCompositeEgressRequest_Preset{
						Preset: test.preset,
					}
				}

				req := &rpc.StartEgressRequest{
					EgressId: utils.NewGuid(utils.EgressPrefix),
					Request: &rpc.StartEgressRequest_RoomComposite{
						RoomComposite: roomRequest,
					},
				}

				r.runFileTest(t, req, test)
			})
			if r.Short {
				return
			}
		}
	})
}

func (r *Runner) testRoomCompositeStream(t *testing.T) {
	if !r.should(runStream) {
		return
	}

	r.runRoomTest(t, "1B/RoomComposite/Stream", types.MimeTypeOpus, types.MimeTypeVP8, func(t *testing.T) {
		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &rpc.StartEgressRequest_RoomComposite{
				RoomComposite: &livekit.RoomCompositeEgressRequest{
					RoomName: r.room.Name(),
					Layout:   "grid-light",
					StreamOutputs: []*livekit.StreamOutput{{
						Protocol: livekit.StreamProtocol_RTMP,
						Urls:     []string{streamUrl1, badStreamUrl1},
					}},
				},
			},
		}

		r.runStreamTest(t, req, &testCase{expectVideoEncoding: true})
	})
}

func (r *Runner) testRoomCompositeSegments(t *testing.T) {
	if !r.should(runSegments) {
		return
	}

	r.runRoomTest(t, "1C/RoomComposite/Segments", types.MimeTypeOpus, types.MimeTypeVP8, func(t *testing.T) {
		for _, test := range []*testCase{
			{
				options: &livekit.EncodingOptions{
					AudioCodec:   livekit.AudioCodec_AAC,
					VideoCodec:   livekit.VideoCodec_H264_BASELINE,
					Width:        1920,
					Height:       1080,
					VideoBitrate: 4500,
				},
				filename:            "r_{room_name}_{time}",
				playlist:            "r_{room_name}_{time}.m3u8",
				livePlaylist:        "r_live_{room_name}_{time}.m3u8",
				filenameSuffix:      livekit.SegmentedFileSuffix_TIMESTAMP,
				expectVideoEncoding: true,
			},
			{
				options: &livekit.EncodingOptions{
					AudioCodec: livekit.AudioCodec_AAC,
				},
				filename:       "r_{room_name}_audio_{time}",
				playlist:       "r_{room_name}_audio_{time}.m3u8",
				filenameSuffix: livekit.SegmentedFileSuffix_TIMESTAMP,
				audioOnly:      true,
			},
		} {
			var segmentOutput *livekit.SegmentedFileOutput
			if test.filenameSuffix == livekit.SegmentedFileSuffix_INDEX && r.GCPUpload != nil {
				segmentOutput = &livekit.SegmentedFileOutput{
					FilenamePrefix:   path.Join(uploadPrefix, test.filename),
					PlaylistName:     test.playlist,
					LivePlaylistName: test.livePlaylist,
					FilenameSuffix:   test.filenameSuffix,
					Output: &livekit.SegmentedFileOutput_Gcp{
						Gcp: r.GCPUpload,
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

			room := &livekit.RoomCompositeEgressRequest{
				RoomName:       r.RoomName,
				Layout:         "grid-dark",
				AudioOnly:      test.audioOnly,
				SegmentOutputs: []*livekit.SegmentedFileOutput{segmentOutput},
			}
			if test.options != nil {
				room.Options = &livekit.RoomCompositeEgressRequest_Advanced{
					Advanced: test.options,
				}
			}

			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_RoomComposite{
					RoomComposite: room,
				},
			}

			r.runSegmentsTest(t, req, test)
		}
	})
}

func (r *Runner) testRoomCompositeImages(t *testing.T) {
	if !r.should(runImages) {
		return
	}

	r.runRoomTest(t, "1D/RoomComposite/Images", types.MimeTypeOpus, types.MimeTypeH264, func(t *testing.T) {
		for _, test := range []*testCase{
			{
				options: &livekit.EncodingOptions{
					Width:  640,
					Height: 360,
				},
				filename:            "r_{room_name}_{time}",
				imageFilenameSuffix: livekit.ImageFileSuffix_IMAGE_SUFFIX_TIMESTAMP,
			},
		} {
			imageOutput := &livekit.ImageOutput{
				FilenamePrefix: path.Join(r.FilePrefix, test.filename),
				FilenameSuffix: test.imageFilenameSuffix,
			}

			room := &livekit.RoomCompositeEgressRequest{
				RoomName:     r.RoomName,
				Layout:       "grid-dark",
				AudioOnly:    test.audioOnly,
				ImageOutputs: []*livekit.ImageOutput{imageOutput},
			}
			if test.options != nil {
				room.Options = &livekit.RoomCompositeEgressRequest_Advanced{
					Advanced: test.options,
				}
			}

			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_RoomComposite{
					RoomComposite: room,
				},
			}

			r.runImagesTest(t, req, test)
		}
	})
}

func (r *Runner) testRoomCompositeMulti(t *testing.T) {
	if !r.should(runMulti) {
		return
	}

	r.runRoomTest(t, "1E/RoomComposite/Multi", types.MimeTypeOpus, types.MimeTypeVP8, func(t *testing.T) {
		req := &rpc.StartEgressRequest{
			EgressId: utils.NewGuid(utils.EgressPrefix),
			Request: &rpc.StartEgressRequest_RoomComposite{
				RoomComposite: &livekit.RoomCompositeEgressRequest{
					RoomName: r.room.Name(),
					Layout:   "grid-light",
					FileOutputs: []*livekit.EncodedFileOutput{{
						FileType: livekit.EncodedFileType_MP4,
						Filepath: path.Join(r.FilePrefix, "rc_multiple_{time}"),
					}},
					ImageOutputs: []*livekit.ImageOutput{{
						CaptureInterval: 10,
						Width:           1280,
						Height:          720,
						FilenamePrefix:  path.Join(r.FilePrefix, "rc_image"),
					}},
				},
			},
		}

		r.runMultipleTest(t, req, true, false, false, true, livekit.SegmentedFileSuffix_TIMESTAMP)
	})
}
