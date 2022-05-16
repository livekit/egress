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
	args := []string{
		"-v", "quiet",
		"-hide_banner",
		"-show_format",
		"-show_streams",
		"-print_format", "json",
	}

	if strings.HasSuffix(input, ".raw") {
		args = append(args,
			"-f", "s16le",
			"-ac", "2",
			"-ar", "48k",
		)
	}

	args = append(args, input)

	cmd := exec.Command("ffprobe", args...)
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
		verify(t, url, p, nil, true)
	}
}

func verify(t *testing.T, input string, p *params.Params, res *livekit.EgressInfo, isStream bool) {
	require.NotEmpty(t, p.OutputType)

	info, err := ffprobe(input)
	require.NoError(t, err, "ffprobe error")

	switch p.OutputType {
	case params.OutputTypeRaw:
		require.Equal(t, 0, info.Format.ProbeScore)
	case params.OutputTypeIVF:
		require.Equal(t, 98, info.Format.ProbeScore)
	default:
		require.Equal(t, 100, info.Format.ProbeScore)
	}

	if !isStream {
		// size
		require.NotEqual(t, "0", info.Format.Size)

		// duration
		expected := float64(res.GetFile().Duration) / 1e9
		actual, err := strconv.ParseFloat(info.Format.Duration, 64)
		require.NoError(t, err)

		// duration can be up to a couple seconds off because the beginning is missing a keyframe
		require.InDelta(t, expected, actual, 3)
	}

	// check stream info
	var hasAudio, hasVideo bool
	for _, stream := range info.Streams {
		switch stream.CodecType {
		case "audio":
			hasAudio = true

			// codec
			switch p.AudioCodec {
			case params.MimeTypeAAC:
				require.Equal(t, "aac", stream.CodecName)
				require.Equal(t, fmt.Sprint(p.AudioFrequency), stream.SampleRate)
				require.Equal(t, "stereo", stream.ChannelLayout)

			case params.MimeTypeOpus:
				require.Equal(t, "opus", stream.CodecName)
				require.Equal(t, "48000", stream.SampleRate)
				require.Equal(t, "stereo", stream.ChannelLayout)

			case params.MimeTypeRaw:
				require.Equal(t, "pcm_s16le", stream.CodecName)
				require.Equal(t, "48000", stream.SampleRate)
			}

			// channels
			require.Equal(t, 2, stream.Channels)

			// audio bitrate
			if p.OutputType == params.OutputTypeMP4 {
				bitrate, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.NotZero(t, bitrate)
				require.Less(t, int32(bitrate), p.AudioBitrate*1100)
			}

		case "video":
			hasVideo = true

			// codec and profile
			switch p.VideoCodec {
			case params.MimeTypeH264:
				require.Equal(t, "h264", stream.CodecName)

				switch p.VideoProfile {
				case params.ProfileBaseline:
					require.Equal(t, "Constrained Baseline", stream.Profile)
				case params.ProfileMain:
					require.Equal(t, "Main", stream.Profile)
				case params.ProfileHigh:
					require.Equal(t, "High", stream.Profile)
				}
			case params.MimeTypeVP8:
				require.Equal(t, "vp8", stream.CodecName)
			}

			switch p.OutputType {
			case params.OutputTypeIVF:
				require.Equal(t, "vp8", stream.CodecName)

			case params.OutputTypeMP4:
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

	if p.AudioEnabled {
		require.True(t, hasAudio)
		require.NotEmpty(t, p.AudioCodec)
	}

	if p.VideoEnabled {
		require.True(t, hasVideo)
		require.NotEmpty(t, p.VideoCodec)
	}
}
