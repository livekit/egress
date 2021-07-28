package config

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/fatih/structs"
	"github.com/stretchr/testify/require"

	livekit "github.com/livekit/livekit-recording/worker/proto"
)

func TestMerge(t *testing.T) {
	defaults := &Config{
		Redis: RedisConfig{},
		Input: &livekit.RecordingInput{
			Width:     1920,
			Height:    1080,
			Depth:     24,
			Framerate: 25,
		},
		Output: &livekit.RecordingOutput{
			AudioBitrate:   "128k",
			AudioFrequency: "44100",
			VideoBitrate:   "2976k",
			VideoBuffer:    "5952k",
		},
	}

	req := &livekit.RecordingReservation{
		Id: "id",
		Input: &livekit.RecordingInput{
			Template: &livekit.RecordingTemplate{
				Type:  "grid",
				WsUrl: "wss://testing.livekit.io",
				Token: "token",
			},
			Framerate: 60,
		},
		Output: &livekit.RecordingOutput{
			File:         "recording.mp4",
			VideoBitrate: "1000k",
			VideoBuffer:  "2000k",
		},
	}

	str, err := Merge(defaults, req)
	require.NoError(t, err)

	merged := &Config{}
	err = json.Unmarshal([]byte(str), merged)
	require.NoError(t, err)

	expected := &Config{
		Input: &livekit.RecordingInput{
			Template: &livekit.RecordingTemplate{
				Type:  "grid",
				WsUrl: "wss://testing.livekit.io",
				Token: "token",
			},
			Width:     1920,
			Height:    1080,
			Depth:     24,
			Framerate: 60,
		},
		Output: &livekit.RecordingOutput{
			File:           "recording.mp4",
			AudioBitrate:   "128k",
			AudioFrequency: "44100",
			VideoBitrate:   "1000k",
			VideoBuffer:    "2000k",
		},
	}
	RequireProtoEqual(t, expected, merged)
}

func RequireProtoEqual(t *testing.T, expected, actual interface{}) {
	a := structs.Fields(actual)
	for i, field := range structs.Fields(expected) {
		if !field.IsExported() {
			continue
		}

		if field.Kind() == reflect.Struct {
			RequireProtoEqual(t, field.Value(), a[i].Value())
		} else {
			require.Equal(t, field.Value(), a[i].Value(), "field: %s", field.Name())
		}
	}
}
