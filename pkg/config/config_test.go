package config_test

import (
	"testing"

	livekit "github.com/livekit/protocol/proto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

var testRequests = []string{`
{
	"template": {
		"layout": "grid-light",
		"token": "recording-token"
	},
	"s3_url": "bucket/path/filename.mp4"
}
`, `
{
	"template": {
		"layout": "speaker-dark",
		"room_name": "test-room"
	},
	"file": "/out/filename.mp4",
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
        "width": 1920,
        "height": 1080
	}
}
`}

func TestRequests(t *testing.T) {
	t.Run("template and s3", func(t *testing.T) {
		req := &livekit.StartRecordingRequest{}
		require.NoError(t, protojson.Unmarshal([]byte(testRequests[0]), req))
		require.NotNil(t, req.Input)
		require.NotNil(t, req.Output)
		template, ok := req.Input.(*livekit.StartRecordingRequest_Template)
		require.True(t, ok)
		require.Equal(t, "grid-light", template.Template.Layout)
		token, ok := template.Template.Room.(*livekit.RecordingTemplate_Token)
		require.True(t, ok)
		require.Equal(t, "recording-token", token.Token)
		s3, ok := req.Output.(*livekit.StartRecordingRequest_S3Url)
		require.True(t, ok)
		require.Equal(t, "bucket/path/filename.mp4", s3.S3Url)
	})

	t.Run("file and preset", func(t *testing.T) {
		req := &livekit.StartRecordingRequest{}
		require.NoError(t, protojson.Unmarshal([]byte(testRequests[1]), req))
		require.NotNil(t, req.Input)
		require.NotNil(t, req.Output)
		require.NotNil(t, req.Options)
		template, ok := req.Input.(*livekit.StartRecordingRequest_Template)
		require.True(t, ok)
		require.Equal(t, "speaker-dark", template.Template.Layout)
		roomName, ok := template.Template.Room.(*livekit.RecordingTemplate_RoomName)
		require.True(t, ok)
		require.Equal(t, "test-room", roomName.RoomName)
		file, ok := req.Output.(*livekit.StartRecordingRequest_File)
		require.True(t, ok)
		require.Equal(t, "/out/filename.mp4", file.File)
		require.Equal(t, livekit.RecordingPreset_FULL_HD_30, req.Options.Preset)
	})

	t.Run("rtmp and options", func(t *testing.T) {
		req := &livekit.StartRecordingRequest{}
		require.NoError(t, protojson.Unmarshal([]byte(testRequests[2]), req))
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
		require.Equal(t, int32(1920), req.Options.Width)
	})
}
