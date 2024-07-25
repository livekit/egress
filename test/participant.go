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
	"time"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

func (r *Runner) testParticipant(t *testing.T) {
	if !r.should(runParticipant) {
		return
	}

	r.sourceFramerate = 23.97
	t.Run("Participant", func(t *testing.T) {
		r.testParticipantFile(t)
		r.testParticipantStream(t)
		r.testParticipantSegments(t)
		r.testParticipantMulti(t)
	})
}

func (r *Runner) runParticipantTest(
	t *testing.T, name string, test *testCase,
	f func(t *testing.T, identity string),
) {
	r.run(t, name, func(t *testing.T) {
		r.publishSampleOffset(t, test.audioCodec, test.audioDelay, test.audioUnpublish)
		if test.audioRepublish != 0 {
			r.publishSampleOffset(t, test.audioCodec, test.audioRepublish, 0)
		}
		r.publishSampleOffset(t, test.videoCodec, test.videoDelay, test.videoUnpublish)
		if test.videoRepublish != 0 {
			r.publishSampleOffset(t, test.videoCodec, test.videoRepublish, 0)
		}
		f(t, r.room.LocalParticipant.Identity())
	})
}

func (r *Runner) testParticipantFile(t *testing.T) {
	if !r.should(runFile) {
		return
	}

	for _, test := range []*testCase{
		{
			name:           "File/VP8",
			fileType:       livekit.EncodedFileType_MP4,
			audioCodec:     types.MimeTypeOpus,
			audioDelay:     time.Second * 8,
			audioUnpublish: time.Second * 14,
			audioRepublish: time.Second * 20,
			videoCodec:     types.MimeTypeVP8,
			filename:       "participant_{publisher_identity}_vp8_{time}.mp4",
		},
		{
			name:           "File/H264",
			fileType:       livekit.EncodedFileType_MP4,
			audioCodec:     types.MimeTypeOpus,
			videoCodec:     types.MimeTypeH264,
			videoUnpublish: time.Second * 10,
			videoRepublish: time.Second * 20,
			filename:       "participant_{room_name}_h264_{time}.mp4",
		},
		{
			name:           "File/AudioOnly",
			fileType:       livekit.EncodedFileType_MP4,
			audioCodec:     types.MimeTypeOpus,
			audioUnpublish: time.Second * 10,
			audioRepublish: time.Second * 15,
			filename:       "participant_{room_name}_{time}.mp4",
		},
	} {
		r.runParticipantTest(t, test.name, test, func(t *testing.T, identity string) {
			var fileOutput *livekit.EncodedFileOutput
			if test.filenameSuffix == livekit.SegmentedFileSuffix_INDEX && r.AzureUpload != nil {
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

			participantRequest := &livekit.ParticipantEgressRequest{
				RoomName:    r.room.Name(),
				Identity:    identity,
				FileOutputs: []*livekit.EncodedFileOutput{fileOutput},
			}
			if test.options != nil {
				participantRequest.Options = &livekit.ParticipantEgressRequest_Advanced{
					Advanced: test.options,
				}
			}

			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_Participant{
					Participant: participantRequest,
				},
			}

			test.expectVideoEncoding = true
			r.runFileTest(t, req, test)
		})
		if r.Short {
			return
		}
	}
}

func (r *Runner) testParticipantStream(t *testing.T) {
	if !r.should(runStream) {
		return
	}

	test := &testCase{
		audioCodec: types.MimeTypeOpus,
		audioDelay: time.Second * 8,
		videoCodec: types.MimeTypeVP8,
	}

	r.runParticipantTest(t, "Stream", test,
		func(t *testing.T, identity string) {
			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_Participant{
					Participant: &livekit.ParticipantEgressRequest{
						RoomName: r.room.Name(),
						Identity: identity,
						StreamOutputs: []*livekit.StreamOutput{{
							Urls: []string{rtmpUrl1, badRtmpUrl1},
						}},
					},
				},
			}

			r.runStreamTest(t, req, &testCase{
				expectVideoEncoding: true,
				outputType:          types.OutputTypeRTMP,
			})
		},
	)
}

func (r *Runner) testParticipantSegments(t *testing.T) {
	if !r.should(runSegments) {
		return
	}

	for _, test := range []*testCase{
		{
			name:       "Segments/VP8",
			audioCodec: types.MimeTypeOpus,
			videoCodec: types.MimeTypeVP8,
			// videoDelay:     time.Second * 10,
			// videoUnpublish: time.Second * 20,
			filename: "participant_{publisher_identity}_vp8_{time}",
			playlist: "participant_{publisher_identity}_vp8_{time}.m3u8",
		},
		{
			name:           "Segments/H264",
			audioCodec:     types.MimeTypeOpus,
			audioDelay:     time.Second * 10,
			audioUnpublish: time.Second * 20,
			videoCodec:     types.MimeTypeH264,
			filename:       "participant_{room_name}_h264_{time}",
			playlist:       "participant_{room_name}_h264_{time}.m3u8",
		},
	} {
		r.runParticipantTest(t, test.name, test,
			func(t *testing.T, identity string) {
				var segmentOutput *livekit.SegmentedFileOutput
				if test.filenameSuffix == livekit.SegmentedFileSuffix_INDEX && r.S3Upload != nil {
					segmentOutput = &livekit.SegmentedFileOutput{
						FilenamePrefix: path.Join(uploadPrefix, test.filename),
						PlaylistName:   test.playlist,
						FilenameSuffix: test.filenameSuffix,
						Output: &livekit.SegmentedFileOutput_S3{
							S3: r.S3Upload,
						},
					}
				} else {
					segmentOutput = &livekit.SegmentedFileOutput{
						FilenamePrefix: path.Join(r.FilePrefix, test.filename),
						PlaylistName:   test.playlist,
						FilenameSuffix: test.filenameSuffix,
					}
				}

				trackRequest := &livekit.ParticipantEgressRequest{
					RoomName:       r.room.Name(),
					Identity:       identity,
					SegmentOutputs: []*livekit.SegmentedFileOutput{segmentOutput},
				}
				if test.options != nil {
					trackRequest.Options = &livekit.ParticipantEgressRequest_Advanced{
						Advanced: test.options,
					}
				}

				req := &rpc.StartEgressRequest{
					EgressId: utils.NewGuid(utils.EgressPrefix),
					Request: &rpc.StartEgressRequest_Participant{
						Participant: trackRequest,
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
}

func (r *Runner) testParticipantMulti(t *testing.T) {
	if !r.should(runMulti) {
		return
	}

	test := &testCase{
		audioCodec:     types.MimeTypeOpus,
		audioUnpublish: time.Second * 20,
		videoCodec:     types.MimeTypeVP8,
		videoDelay:     time.Second * 5,
	}

	r.runParticipantTest(t, "Multi", test,
		func(t *testing.T, identity string) {
			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_Participant{
					Participant: &livekit.ParticipantEgressRequest{
						RoomName: r.room.Name(),
						Identity: identity,
						FileOutputs: []*livekit.EncodedFileOutput{{
							FileType: livekit.EncodedFileType_MP4,
							Filepath: path.Join(r.FilePrefix, "participant_multiple_{time}"),
						}},
						StreamOutputs: []*livekit.StreamOutput{{
							Protocol: livekit.StreamProtocol_RTMP,
						}},
					},
				},
			}

			r.runMultipleTest(t, req, true, true, false, false, livekit.SegmentedFileSuffix_INDEX)
		},
	)
}
