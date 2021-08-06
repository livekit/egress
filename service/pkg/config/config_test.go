package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	livekit "github.com/livekit/livekit-recorder/service/proto"
)

func TestMerge(t *testing.T) {
	defaults := &Config{
		Redis: RedisConfig{},
		Options: &livekit.RecordingOptions{
			InputWidth:     1920,
			InputHeight:    1080,
			Depth:          24,
			Framerate:      30,
			AudioBitrate:   128,
			AudioFrequency: 44100,
			VideoBitrate:   4500,
		},
	}

	req := &livekit.RecordingReservation{
		Id: "id",
		Request: &livekit.StartRecordingRequest{
			Input: &livekit.StartRecordingRequest_Template{
				Template: &livekit.RecordingTemplate{
					Layout: "grid-dark",
					WsUrl:  "wss://testing.livekit.io",
					Token:  "token",
				},
			},
			Output: &livekit.StartRecordingRequest_File{
				File: "recording.mp4",
			},
			Options: &livekit.RecordingOptions{
				Framerate:    60,
				VideoBitrate: 6000,
			},
		},
	}

	merged, err := Merge(defaults, req)
	require.NoError(t, err)
	expected := "{\"input\":{\"template\":{\"layout\":\"grid-dark\",\"ws_url\":\"wss://testing.livekit.io\",\"token\":\"token\"}},\"options\":{\"audio_bitrate\":128,\"audio_frequency\":44100,\"depth\":24,\"framerate\":60,\"input_height\":1080,\"input_width\":1920,\"video_bitrate\":6000},\"output\":{\"file\":\"recording.mp4\"}}"
	require.Equal(t, expected, merged)

	req = &livekit.RecordingReservation{
		Id: "id",
		Request: &livekit.StartRecordingRequest{
			Input: &livekit.StartRecordingRequest_Template{
				Template: &livekit.RecordingTemplate{
					Layout: "grid-dark",
					WsUrl:  "wss://testing.livekit.io",
					Token:  "token",
				},
			},
			Output: &livekit.StartRecordingRequest_File{
				File: "recording.mp4",
			},
		},
	}

	merged, err = Merge(defaults, req)
	require.NoError(t, err)
	expected = "{\"input\":{\"template\":{\"layout\":\"grid-dark\",\"ws_url\":\"wss://testing.livekit.io\",\"token\":\"token\"}},\"options\":{\"input_width\":1920,\"input_height\":1080,\"depth\":24,\"framerate\":30,\"audio_bitrate\":128,\"audio_frequency\":44100,\"video_bitrate\":4500},\"output\":{\"file\":\"recording.mp4\"}}"
	require.Equal(t, expected, merged)
}
