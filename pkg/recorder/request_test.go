package recorder

import (
	"strings"
	"testing"

	livekit "github.com/livekit/protocol/proto"
	"github.com/stretchr/testify/require"

	"github.com/livekit/livekit-recorder/pkg/config"
)

func TestInputUrl(t *testing.T) {
	req := &livekit.StartRecordingRequest{
		Input: &livekit.StartRecordingRequest_Template{
			Template: &livekit.RecordingTemplate{
				Layout:   "speaker-light",
				RoomName: "hello",
			},
		},
	}

	conf, err := config.TestConfig()
	conf.WsUrl = "wss://fake.url.io"
	require.NoError(t, err)
	rec := NewRecorder(conf, "fakeRecordingID")

	actual, err := rec.GetInputUrl(req)
	require.NoError(t, err)
	expected := "https://recorder.livekit.io/#/speaker-light?url=wss%3A%2F%2Ffake.url.io&token="
	require.True(t, strings.HasPrefix(actual, expected), actual)
}
