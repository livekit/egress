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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

func (r *Runner) testFile(t *testing.T) {
	if !r.should(runFile) {
		return
	}

	t.Run("File", func(t *testing.T) {
		for _, test := range []*testCase{

			// ---- Room Composite -----

			{
				name:        "RoomComposite/Base",
				requestType: types.RequestTypeRoomComposite, publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeH264,
					layout:     "speaker",
				},
				fileOptions: &fileOptions{
					filename: "r_{room_name}_{time}.mp4",
				},
				contentCheck: r.fullContentCheck,
			},
			{
				name:        "RoomComposite/VideoOnly",
				requestType: types.RequestTypeRoomComposite, publishOptions: publishOptions{
					videoCodec: types.MimeTypeH264,
					videoOnly:  true,
					layout:     "speaker",
				},
				encodingOptions: &livekit.EncodingOptions{
					VideoCodec: livekit.VideoCodec_H264_HIGH,
				},
				fileOptions: &fileOptions{
					filename: "r_{room_name}_video_{time}.mp4",
				},
				contentCheck: r.videoOnlyContentCheck,
			},
			{
				name:        "RoomComposite/AudioOnly",
				requestType: types.RequestTypeRoomComposite, publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					audioOnly:  true,
				},
				encodingOptions: &livekit.EncodingOptions{
					AudioCodec: livekit.AudioCodec_OPUS,
				},
				fileOptions: &fileOptions{
					filename: "r_{room_name}_audio_{time}",
					fileType: livekit.EncodedFileType_OGG,
				},
				contentCheck: r.audioOnlyContentCheck,
			},
			{
				name:        "RoomComposite/AudioOnlyMP3",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					audioOnly:  true,
				},
				fileOptions: &fileOptions{
					filename: "r_{room_name}_audio_mp3_{time}",
					fileType: livekit.EncodedFileType_MP3,
				},
				contentCheck: r.audioOnlyContentCheck,
			},

			// ---------- Web ----------

			{
				name: "Web",
				publishOptions: publishOptions{
					videoOnly: true,
				},
				requestType: types.RequestTypeWeb,
				fileOptions: &fileOptions{
					filename: "web_{time}",
				},
			},

			// ------ Participant ------

			{
				name:        "ParticipantComposite/VP8",
				requestType: types.RequestTypeParticipant, publishOptions: publishOptions{
					audioCodec:     types.MimeTypeOpus,
					audioDelay:     time.Second * 8,
					audioUnpublish: time.Second * 14,
					audioRepublish: time.Second * 20,
					videoCodec:     types.MimeTypeVP8,
				},
				fileOptions: &fileOptions{
					filename: "participant_{publisher_identity}_vp8_{time}.mp4",
					fileType: livekit.EncodedFileType_MP4,
				},
			},
			{
				name:        "ParticipantComposite/H264",
				requestType: types.RequestTypeParticipant, publishOptions: publishOptions{
					audioCodec:     types.MimeTypeOpus,
					videoCodec:     types.MimeTypeH264,
					videoUnpublish: time.Second * 10,
					videoRepublish: time.Second * 20,
				},
				fileOptions: &fileOptions{
					filename: "participant_{room_name}_h264_{time}.mp4",
					fileType: livekit.EncodedFileType_MP4,
				},
				contentCheck: r.fullContentCheckWithVideoUnpublishAt10AndRepublishAt20,
			},
			{
				name:        "ParticipantComposite/AudioOnly",
				requestType: types.RequestTypeParticipant, publishOptions: publishOptions{
					audioCodec:     types.MimeTypeOpus,
					audioUnpublish: time.Second * 10,
					audioRepublish: time.Second * 15,
				},
				fileOptions: &fileOptions{
					filename: "participant_{room_name}_{time}.mp4",
					fileType: livekit.EncodedFileType_MP4,
				},
			},

			// ---- Track Composite ----

			{
				name:        "TrackComposite/VP8",
				requestType: types.RequestTypeTrackComposite, publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeVP8,
				},
				fileOptions: &fileOptions{
					filename: "tc_{publisher_identity}_vp8_{time}.mp4",
					fileType: livekit.EncodedFileType_MP4,
				},
				contentCheck: r.fullContentCheck,
			},
			{
				name:        "TrackComposite/VideoOnly",
				requestType: types.RequestTypeTrackComposite,
				publishOptions: publishOptions{
					videoCodec: types.MimeTypeH264,
					videoOnly:  true,
				},
				fileOptions: &fileOptions{
					filename: "tc_{room_name}_video_{time}.mp4",
					fileType: livekit.EncodedFileType_MP4,
				},
				contentCheck: r.videoOnlyContentCheck,
			},
			{
				name:        "TrackComposite/AudioOnlyMP3",
				requestType: types.RequestTypeTrackComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					audioOnly:  true,
				},
				fileOptions: &fileOptions{
					filename:   "tc_{room_name}_audio_mp3_{time}",
					fileType:   livekit.EncodedFileType_MP3,
					outputType: types.OutputTypeMP3,
				},
				contentCheck: r.audioOnlyContentCheck,
			},
			{
				name:        "TrackComposite/AudioOnlyPCMU",
				requestType: types.RequestTypeTrackComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypePCMU,
					audioOnly:  true,
				},
				fileOptions: &fileOptions{
					filename:   "tc_{room_name}_audio_pcmu_{time}.mp4",
					fileType:   livekit.EncodedFileType_MP4,
					outputType: types.OutputTypeMP4,
				},
				contentCheck: r.audioOnlyContentCheck,
			},
			{
				name:        "TrackComposite/AudioOnlyPCMA",
				requestType: types.RequestTypeTrackComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypePCMA,
					audioOnly:  true,
				},
				fileOptions: &fileOptions{
					filename:   "tc_{room_name}_audio_pcma_{time}.mp4",
					fileType:   livekit.EncodedFileType_MP4,
					outputType: types.OutputTypeMP4,
				},
				contentCheck: r.audioOnlyContentCheck,
			},

			// --------- Track ---------

			{
				name:        "Track/Opus",
				requestType: types.RequestTypeTrack,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					audioOnly:  true,
				},
				fileOptions: &fileOptions{
					filename:   "t_{track_source}_{time}.ogg",
					outputType: types.OutputTypeOGG,
				},
				contentCheck: r.audioOnlyContentCheck,
			},
			{
				name:        "Track/PCMU",
				requestType: types.RequestTypeTrack,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypePCMU,
					audioOnly:  true,
				},
				fileOptions: &fileOptions{
					filename:   "t_{track_source}_pcmu_{time}.ogg",
					outputType: types.OutputTypeOGG,
				},
				contentCheck: r.audioOnlyContentCheck,
			},
			{
				name:        "Track/PCMA",
				requestType: types.RequestTypeTrack,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypePCMA,
					audioOnly:  true,
				},
				fileOptions: &fileOptions{
					filename:   "t_{track_source}_pcma_{time}.ogg",
					outputType: types.OutputTypeOGG,
				},
				contentCheck: r.audioOnlyContentCheck,
			},
			{
				name:        "Track/H264",
				requestType: types.RequestTypeTrack,
				publishOptions: publishOptions{
					videoCodec: types.MimeTypeH264,
					videoOnly:  true,
				},

				fileOptions: &fileOptions{
					filename:   "t_{track_id}_{time}.mp4",
					outputType: types.OutputTypeMP4,
				},
				contentCheck: r.videoOnlyContentCheck,
			},
			{
				name:        "Track/VP8",
				requestType: types.RequestTypeTrack,
				publishOptions: publishOptions{
					videoCodec: types.MimeTypeVP8,
					videoOnly:  true,
				},
				fileOptions: &fileOptions{
					filename:   "t_{track_type}_{time}.webm",
					outputType: types.OutputTypeWebM,
				},
				contentCheck: r.videoOnlyContentCheck,
			},
			// {
			// 	name:       "Track/VP9",
			// 	videoOnly:  true,
			// 	videoCodec: types.MimeTypeVP9,
			// 	outputType: types.OutputTypeWebM,
			// 	filename:   "t_{track_type}_{time}.webm",
			// },
		} {
			if !r.run(t, test, r.runFileTest) {
				return
			}
		}
	})
}

func (r *Runner) runFileTest(t *testing.T, test *testCase) {
	req := r.build(test)

	// start
	egressID := r.startEgress(t, req)

	time.Sleep(time.Second * 10)
	if r.Dotfiles {
		r.createDotFile(t, egressID)
	}

	// stop
	time.Sleep(time.Second * 15)
	res := r.stopEgress(t, egressID)

	// get params
	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)
	if p.GetFileConfig().OutputType == types.OutputTypeUnknownFile {
		p.GetFileConfig().OutputType = test.fileOptions.outputType
	}

	require.Equal(t, test.requestType != types.RequestTypeTrack && !test.audioOnly, p.VideoEncoding)

	// verify
	r.verifyFile(t, test, p, res)
}

func (r *Runner) verifyFile(t *testing.T, tc *testCase, p *config.PipelineConfig, res *livekit.EgressInfo) {
	// egress info
	require.Equal(t, res.Error == "", res.Status != livekit.EgressStatus_EGRESS_FAILED)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// file info
	fileRes := res.GetFile() //nolint:staticcheck
	if fileRes == nil {
		require.Len(t, res.FileResults, 1)
		fileRes = res.FileResults[0]
	}

	require.NotEmpty(t, fileRes.Location)
	require.Greater(t, fileRes.Size, int64(0))
	require.Greater(t, fileRes.Duration, int64(0))

	storagePath := fileRes.Filename
	require.NotEmpty(t, storagePath)
	require.False(t, strings.Contains(storagePath, "{"))
	storageFilename := path.Base(storagePath)

	// download from cloud storage
	localPath := path.Join(r.FilePrefix, storageFilename)
	download(t, p.GetFileConfig().StorageConfig, localPath, storagePath, false)

	manifestLocal := path.Join(path.Dir(localPath), res.EgressId+".json")
	manifestStorage := path.Join(path.Dir(storagePath), res.EgressId+".json")
	manifest := loadManifest(t, p.GetFileConfig().StorageConfig, manifestLocal, manifestStorage)
	require.NotNil(t, manifest)

	// verify
	info := verify(t, localPath, p, res, types.EgressTypeFile, r.Muting, r.sourceFramerate, false)

	if tc.contentCheck != nil && info != nil {
		tc.contentCheck(t, localPath, info)
	}
}
