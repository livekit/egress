//go:build integration
// +build integration

package test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-egress/pkg/pipeline/params"
)

type FFProbeInfo struct {
	Streams []struct {
		CodecName string `json:"codec_name"`
		CodecType string `json:"codec_type"`
		Profile   string `json:"profile"`

		// audio
		SampleRate    string `json:"sample_rate"`
		Channels      int    `json:"channels"`
		ChannelLayout string `json:"channel_layout"`

		// video
		Width      int32  `json:"width"`
		Height     int32  `json:"height"`
		RFrameRate string `json:"r_frame_rate"`
		BitRate    string `json:"bit_rate"`
	} `json:"streams"`
	Format struct {
		Filename   string `json:"filename"`
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		Size       string `json:"size"`
		ProbeScore int    `json:"probe_score"`
		Tags       struct {
			Encoder string `json:"encoder"`
		} `json:"tags"`
	} `json:"format"`
}

func ffprobe(input string) (*FFProbeInfo, error) {
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-hide_banner",
		"-show_format",
		"-show_streams",
		"-print_format", "json",
		input,
	)

	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	info := &FFProbeInfo{}
	err = json.Unmarshal(out, info)
	return info, err
}

func verifyStreams(t *testing.T, p *params.Params, urls ...string) {
	for _, url := range urls {
		verify(t, url, p, nil, true, "")
	}
}

func verify(t *testing.T, input string, p *params.Params, res *livekit.EgressInfo, isStream bool, fileType string) {
	info, err := ffprobe(input)
	require.NoError(t, err, "ffprobe error")

	switch fileType {
	case "ivf":
		// sample file also has a probe score of 98
		require.Equal(t, 98, info.Format.ProbeScore)
	case "h264":
		// sample file also has a probe score of 51 (why so low?)
		require.Equal(t, 51, info.Format.ProbeScore)

		// size
		require.NotEqual(t, "0", info.Format.Size)
	default:
		// should normally be 100
		require.Equal(t, 100, info.Format.ProbeScore)
	}

	if !isStream && fileType != "h264" {
		// size
		require.NotEqual(t, "0", info.Format.Size)

		// duration
		expected := float64(res.GetFile().Duration) / 1e9
		actual, err := strconv.ParseFloat(info.Format.Duration, 64)
		require.NoError(t, err)

		delta := 0.5
		if fileType == "ivf" {
			delta = 3
		} else {
			switch res.Request.(type) {
			case *livekit.EgressInfo_TrackComposite:
				if !p.AudioEnabled {
					delta = 3
				}
			}
		}
		require.InDelta(t, expected, actual, delta)
	}

	// check stream info
	var hasAudio, hasVideo bool
	for _, stream := range info.Streams {
		switch stream.CodecType {
		case "audio":
			hasAudio = true

			// codec
			switch {
			case p.AudioCodec == livekit.AudioCodec_AAC:
				require.Equal(t, "aac", stream.CodecName)
				require.Equal(t, fmt.Sprint(p.AudioFrequency), stream.SampleRate)

			case p.AudioCodec == livekit.AudioCodec_OPUS || fileType == "ogg":
				require.Equal(t, "opus", stream.CodecName)
				require.Equal(t, "48000", stream.SampleRate)
			}

			// channels
			require.Equal(t, 2, stream.Channels)
			require.Equal(t, "stereo", stream.ChannelLayout)

			// audio bitrate
			if fileType == livekit.EncodedFileType_MP4.String() {
				bitrate, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.NotZero(t, bitrate)
				require.Less(t, int32(bitrate), p.AudioBitrate*1100)
			}

		case "video":
			hasVideo = true

			// codec and profile
			switch p.VideoCodec {
			case livekit.VideoCodec_H264_BASELINE:
				require.Equal(t, "h264", stream.CodecName)
				require.Equal(t, "Constrained Baseline", stream.Profile)

			case livekit.VideoCodec_H264_MAIN:
				require.Equal(t, "h264", stream.CodecName)
				require.Equal(t, "Main", stream.Profile)

			case livekit.VideoCodec_H264_HIGH:
				require.Equal(t, "h264", stream.CodecName)
				require.Equal(t, "High", stream.Profile)
			}

			switch fileType {
			case "h264":
				require.Equal(t, "h264", stream.CodecName)

			case "ivf":
				require.Equal(t, "vp8", stream.CodecName)

			case livekit.EncodedFileType_MP4.String():
				// bitrate
				bitrate, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.NotZero(t, bitrate)
				require.Less(t, int32(bitrate), p.VideoBitrate*1010)

				// dimensions
				require.Equal(t, p.Width, stream.Width)
				require.Equal(t, p.Height, stream.Height)

				// framerate
				frac := strings.Split(stream.RFrameRate, "/")
				require.Len(t, frac, 2)
				n, err := strconv.ParseFloat(frac[0], 64)
				require.NoError(t, err)
				d, err := strconv.ParseFloat(frac[1], 64)
				require.NoError(t, err)
				require.Greater(t, n/d, float64(p.Framerate)*0.95)
			}

		default:
			t.Fatalf("unrecognized stream type %s", stream.CodecType)
		}
	}

	require.Equal(t, p.AudioEnabled, hasAudio)
	require.Equal(t, p.VideoEnabled, hasVideo)
}
