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

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
)

func TestRedactUpload(t *testing.T) {
	t.Cleanup(func() {
		_ = os.Remove("test_upload/")
	})

	conf := &ServiceConfig{
		BaseConfig: BaseConfig{
			NodeID: "server",
		},
	}

	fileReq := &rpc.StartEgressRequest{
		EgressId: "test_upload",
		Request: &rpc.StartEgressRequest_RoomComposite{
			RoomComposite: &livekit.RoomCompositeEgressRequest{
				RoomName: "room",
				Layout:   "layout",
				Output: &livekit.RoomCompositeEgressRequest_File{
					File: &livekit.EncodedFileOutput{
						Filepath: "filepath",
						Output: &livekit.EncodedFileOutput_S3{
							S3: &livekit.S3Upload{
								AccessKey: "access",
								Secret:    "secret",
								Bucket:    "bucket",
							},
						},
					},
				},
			},
		},
		Token: "token",
		WsUrl: "wss://egress.com",
	}

	p, err := GetValidatedPipelineConfig(conf, fileReq)
	require.NoError(t, err)

	require.Equal(t, "******", p.Info.GetRoomComposite().GetFile().GetS3().AccessKey)

	require.Len(t, p.Outputs, 1)
	output := p.GetFileConfig()
	require.NotNil(t, output.UploadConfig)
	require.Equal(t, "access", output.UploadConfig.(*livekit.S3Upload).AccessKey)
}

func TestRedactStreamKeys(t *testing.T) {
	t.Cleanup(func() {
		_ = os.Remove("test_stream/")
	})

	var (
		streamUrl1   = "rtmp://sfo.contribute.live-video.net/app/stream_key"
		redactedUrl1 = "rtmp://sfo.contribute.live-video.net/app/**********"
		streamUrl2   = "rtmps://live-api-s.facebook.com:443/rtmp/stream_key"
		redactedUrl2 = "rtmps://live-api-s.facebook.com:443/rtmp/**********"
	)

	conf := &ServiceConfig{
		BaseConfig: BaseConfig{
			NodeID: "server",
		},
	}

	streamReq := &rpc.StartEgressRequest{
		EgressId: "test_stream",
		Request: &rpc.StartEgressRequest_RoomComposite{
			RoomComposite: &livekit.RoomCompositeEgressRequest{
				RoomName: "room",
				Layout:   "layout",
				Output: &livekit.RoomCompositeEgressRequest_Stream{
					Stream: &livekit.StreamOutput{
						Urls: []string{
							streamUrl1,
							streamUrl2,
						},
					},
				},
			},
		},
		Token: "token",
		WsUrl: "wss://egress.com",
	}

	p, err := GetValidatedPipelineConfig(conf, streamReq)
	require.NoError(t, err)

	urls := p.Info.GetRoomComposite().GetStream().GetUrls()
	require.Len(t, urls, 2)
	require.Equal(t, redactedUrl1, urls[0])
	require.Equal(t, redactedUrl2, urls[1])

	streamInfo := p.Info.GetStream()
	require.Len(t, streamInfo.Info, 2)
	require.Equal(t, redactedUrl1, streamInfo.Info[0].Url)
	require.Equal(t, redactedUrl2, streamInfo.Info[1].Url)

	require.Len(t, p.Outputs, 1)
	output := p.GetStreamConfig()
	require.Len(t, output.Urls, 2)
	require.Equal(t, streamUrl1, output.Urls[0])
	require.Equal(t, streamUrl2, output.Urls[1])
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
		o, err := p.getSegmentConfig(&livekit.SegmentedFileOutput{
			FilenamePrefix:   test.filenamePrefix,
			PlaylistName:     test.playlistName,
			LivePlaylistName: test.livePlaylistName,
		})
		require.NoError(t, err)

		require.Equal(t, test.expectedStorageDir, o.StorageDir)
		require.Equal(t, test.expectedPlaylistFilename, o.PlaylistFilename)
		require.Equal(t, test.expectedLivePlaylistFilename, o.LivePlaylistFilename)
		require.Equal(t, test.expectedSegmentPrefix, o.SegmentPrefix)
	}
}
