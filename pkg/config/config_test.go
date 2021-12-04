package config_test

import (
	"testing"

	livekit "github.com/livekit/protocol/proto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/livekit/livekit-recorder/pkg/config"
)

var testConfig = `
log_level: debug
api_key: key
api_secret: secret
ws_url: wss://localhost:7880
file_output:
  local: true
redis:
  address: 192.168.65.2:6379
defaults:
  width: 320
  height: 200
  depth: 24
  framerate: 10
  audio_bitrate: 96
  audio_frequency: 22050
  video_bitrate: 750
`

var testRequests = []string{`
{
	"template": {
		"layout": "speaker-dark",
		"room_name": "test-room"
	},
	"filepath": "/out/filename.mp4",
	"options": {
		"preset": "FULL_HD_30"
	}
}
`, `
{
	"url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	"rtmp": {
        "urls": ["rtmp://stream-url.com", "rtmp://live.twitch.tv/app/stream-key"]
    },
	"options": {
		"audio_bitrate": 96,
		"audio_frequency": 22050,
		"video_bitrate": 750
	}
}
`}

func TestConfig(t *testing.T) {
	conf, err := config.NewConfig(testConfig)
	require.NoError(t, err)
	require.Equal(t, "192.168.65.2:6379", conf.Redis.Address)
	require.Equal(t, int32(320), conf.Defaults.Width)
	require.Equal(t, int32(96), conf.Defaults.AudioBitrate)
}

func TestRequests(t *testing.T) {
	t.Run("file and preset", func(t *testing.T) {
		req := &livekit.StartRecordingRequest{}
		require.NoError(t, protojson.Unmarshal([]byte(testRequests[0]), req))
		require.NotNil(t, req.Input)
		require.NotNil(t, req.Output)
		require.NotNil(t, req.Options)
		template, ok := req.Input.(*livekit.StartRecordingRequest_Template)
		require.True(t, ok)
		require.Equal(t, "speaker-dark", template.Template.Layout)
		require.Equal(t, "test-room", template.Template.RoomName)
		filepath := req.Output.(*livekit.StartRecordingRequest_Filepath).Filepath
		require.True(t, ok)
		require.Equal(t, "/out/filename.mp4", filepath)
		require.Equal(t, livekit.RecordingPreset_FULL_HD_30, req.Options.Preset)
	})

	t.Run("rtmp and options", func(t *testing.T) {
		req := &livekit.StartRecordingRequest{}
		require.NoError(t, protojson.Unmarshal([]byte(testRequests[1]), req))
		require.NotNil(t, req.Input)
		require.NotNil(t, req.Output)
		require.NotNil(t, req.Options)
		url, ok := req.Input.(*livekit.StartRecordingRequest_Url)
		require.True(t, ok)
		require.Equal(t, "https://www.youtube.com/watch?v=dQw4w9WgXcQ", url.Url)
		rtmp, ok := req.Output.(*livekit.StartRecordingRequest_Rtmp)
		require.True(t, ok)
		expected := []string{"rtmp://stream-url.com", "rtmp://live.twitch.tv/app/stream-key"}
		require.Equal(t, expected, rtmp.Rtmp.Urls)
		require.Equal(t, int32(96), req.Options.AudioBitrate)
		require.Equal(t, int32(22050), req.Options.AudioFrequency)
		require.Equal(t, int32(750), req.Options.VideoBitrate)
	})
}
