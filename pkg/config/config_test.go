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

package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

func TestS3RequestAssumeRoleExternalIDGate(t *testing.T) {
	makeReq := func(externalID string) *livekit.EncodedFileOutput {
		return &livekit.EncodedFileOutput{
			Output: &livekit.EncodedFileOutput_S3{
				S3: &livekit.S3Upload{
					AccessKey:            "ACCESS_KEY",
					Secret:               "SECRET",
					Bucket:               "bucket",
					Region:               "us-east-1",
					AssumeRoleArn:        "arn:aws:iam::123456789012:role/example",
					AssumeRoleExternalId: externalID,
				},
			},
		}
	}

	t.Run("rejects request external_id when flag is false", func(t *testing.T) {
		p := &PipelineConfig{}
		_, err := p.getStorageConfig(makeReq("EXT_ID"))
		require.Error(t, err)
		require.ErrorContains(t, err, "S3 AssumeRoleExternalId from request")
	})

	t.Run("allows request external_id when flag is true", func(t *testing.T) {
		p := &PipelineConfig{}
		p.S3AllowRequestAssumeRoleExternalID = true
		sc, err := p.getStorageConfig(makeReq("EXT_ID"))
		require.NoError(t, err)
		require.NotNil(t, sc.S3)
		require.Equal(t, "EXT_ID", sc.S3.AssumeRoleExternalId)
	})

	t.Run("no rejection when request omits external_id", func(t *testing.T) {
		p := &PipelineConfig{}
		sc, err := p.getStorageConfig(makeReq(""))
		require.NoError(t, err)
		require.NotNil(t, sc.S3)
		require.Equal(t, "", sc.S3.AssumeRoleExternalId)
	})

	t.Run("server-side default external_id still applies when request omits it", func(t *testing.T) {
		p := &PipelineConfig{}
		p.S3AssumeRoleExternalID = "SERVER_SIDE_EXT_ID"
		req := &livekit.EncodedFileOutput{
			Output: &livekit.EncodedFileOutput_S3{
				S3: &livekit.S3Upload{
					AccessKey: "ACCESS_KEY",
					Secret:    "SECRET",
					Bucket:    "bucket",
					Region:    "us-east-1",
				},
			},
		}
		sc, err := p.getStorageConfig(req)
		require.NoError(t, err)
		require.NotNil(t, sc.S3)
		require.Equal(t, "SERVER_SIDE_EXT_ID", sc.S3.AssumeRoleExternalId)
	})
}

func TestSegmentNaming(t *testing.T) {
	t.Cleanup(func() {
		_ = os.RemoveAll("conf_test/")
	})

	for _, test := range []struct {
		filenamePrefix               string
		playlistName                 string
		livePlaylistName             string
		expectedStorageDir           string
		expectedPlaylistFilename     string
		expectedLivePlaylistFilename string
		expectedSegmentPrefix        string
	}{
		{
			filenamePrefix: "", playlistName: "playlist", livePlaylistName: "",
			expectedStorageDir: "", expectedPlaylistFilename: "playlist.m3u8", expectedLivePlaylistFilename: "", expectedSegmentPrefix: "playlist",
		},
		{
			filenamePrefix: "", playlistName: "conf_test/playlist", livePlaylistName: "conf_test/live_playlist",
			expectedStorageDir: "conf_test/", expectedPlaylistFilename: "playlist.m3u8", expectedLivePlaylistFilename: "live_playlist.m3u8", expectedSegmentPrefix: "playlist",
		},
		{
			filenamePrefix: "filename", playlistName: "", livePlaylistName: "live_playlist2.m3u8",
			expectedStorageDir: "", expectedPlaylistFilename: "filename.m3u8", expectedLivePlaylistFilename: "live_playlist2.m3u8", expectedSegmentPrefix: "filename",
		},
		{
			filenamePrefix: "filename", playlistName: "playlist", livePlaylistName: "",
			expectedStorageDir: "", expectedPlaylistFilename: "playlist.m3u8", expectedLivePlaylistFilename: "", expectedSegmentPrefix: "filename",
		},
		{
			filenamePrefix: "filename", playlistName: "conf_test/", livePlaylistName: "",
			expectedStorageDir: "conf_test/", expectedPlaylistFilename: "filename.m3u8", expectedLivePlaylistFilename: "", expectedSegmentPrefix: "filename",
		},
		{
			filenamePrefix: "filename", playlistName: "conf_test/playlist", livePlaylistName: "",
			expectedStorageDir: "conf_test/", expectedPlaylistFilename: "playlist.m3u8", expectedLivePlaylistFilename: "", expectedSegmentPrefix: "filename",
		},
		{
			filenamePrefix: "conf_test/", playlistName: "playlist", livePlaylistName: "",
			expectedStorageDir: "conf_test/", expectedPlaylistFilename: "playlist.m3u8", expectedLivePlaylistFilename: "", expectedSegmentPrefix: "playlist",
		},
		{
			filenamePrefix: "conf_test/filename", playlistName: "playlist", livePlaylistName: "",
			expectedStorageDir: "conf_test/", expectedPlaylistFilename: "playlist.m3u8", expectedLivePlaylistFilename: "", expectedSegmentPrefix: "filename",
		},
		{
			filenamePrefix: "conf_test/filename", playlistName: "conf_test/playlist", livePlaylistName: "",
			expectedStorageDir: "conf_test/", expectedPlaylistFilename: "playlist.m3u8", expectedLivePlaylistFilename: "", expectedSegmentPrefix: "filename",
		},
		{
			filenamePrefix: "conf_test_2/filename", playlistName: "conf_test/playlist", livePlaylistName: "",
			expectedStorageDir: "conf_test/", expectedPlaylistFilename: "playlist.m3u8", expectedLivePlaylistFilename: "", expectedSegmentPrefix: "conf_test_2/filename",
		},
	} {
		p := &PipelineConfig{Info: &livekit.EgressInfo{EgressId: "egress_ID"}}
		seg := &livekit.SegmentedFileOutput{
			FilenamePrefix:   test.filenamePrefix,
			PlaylistName:     test.playlistName,
			LivePlaylistName: test.livePlaylistName,
		}
		o, err := p.getSegmentConfig(seg, seg)
		require.NoError(t, err)

		require.Equal(t, test.expectedStorageDir, o.StorageDir)
		require.Equal(t, test.expectedPlaylistFilename, o.PlaylistFilename)
		require.Equal(t, test.expectedLivePlaylistFilename, o.LivePlaylistFilename)
		require.Equal(t, test.expectedSegmentPrefix, o.SegmentPrefix)
	}
}

func TestValidateAndUpdateOutputParamsRejectsHLSMP3(t *testing.T) {
	p := &PipelineConfig{
		Outputs: map[types.EgressType][]OutputConfig{
			types.EgressTypeSegments: {
				&SegmentConfig{outputConfig: outputConfig{OutputType: types.OutputTypeHLS}},
			},
		},
	}

	p.AudioEnabled = true
	p.VideoEnabled = false
	p.AudioOutCodec = types.MimeTypeMP3
	p.Info = &livekit.EgressInfo{}

	err := p.validateAndUpdateOutputParams()
	require.Error(t, err)
	require.ErrorContains(t, err, "format application/x-mpegurl incompatible with codec audio/mpeg")
}

func TestValidateAndUpdateOutputParamsRejectsVideoFileMP3(t *testing.T) {
	p := &PipelineConfig{
		Outputs: map[types.EgressType][]OutputConfig{
			types.EgressTypeFile: {
				&FileConfig{outputConfig: outputConfig{OutputType: types.OutputTypeMP3}},
			},
		},
	}

	p.AudioEnabled = true
	p.VideoEnabled = true
	p.AudioOutCodec = types.MimeTypeMP3
	p.VideoOutCodec = types.MimeTypeH264
	p.Info = &livekit.EgressInfo{}

	err := p.validateAndUpdateOutputParams()
	require.Error(t, err)
	require.ErrorContains(t, err, "format audio/mpeg incompatible with codec video/h264")
}
