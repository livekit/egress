//go:build integration

package test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/input/builder"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

type ResultType int

const (
	ResultTypeFile ResultType = iota
	ResultTypeStream
	ResultTypeSegments

	maxRetries = 5
	minDelay   = time.Millisecond * 100
	maxDelay   = time.Second * 5
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

func verifyFile(t *testing.T, conf *TestConfig, p *config.PipelineConfig, res *livekit.EgressInfo) {
	// egress info
	require.Equal(t, res.Error == "", res.Status != livekit.EgressStatus_EGRESS_FAILED)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// file info
	fileRes := res.GetFile()
	require.NotNil(t, fileRes)

	require.NotEmpty(t, fileRes.Location)
	require.Greater(t, fileRes.Size, int64(0))
	require.Greater(t, fileRes.Duration, int64(0))

	storagePath := fileRes.Filename
	localPath := fileRes.Filename
	require.NotEmpty(t, storagePath)
	require.False(t, strings.Contains(storagePath, "{"))

	// download from cloud storage
	if p.UploadConfig != nil {
		localPath = fmt.Sprintf("%s/%s", conf.LocalOutputDirectory, storagePath)
		download(t, p.UploadConfig, localPath, storagePath)
		download(t, p.UploadConfig, localPath+".json", storagePath+".json")
	}

	// verify
	verify(t, localPath, p, res, ResultTypeFile, conf.Muting, conf.sourceFramerate)
}

func verifyStreams(t *testing.T, p *config.PipelineConfig, conf *TestConfig, urls ...string) {
	for _, url := range urls {
		verify(t, url, p, nil, ResultTypeStream, false, conf.sourceFramerate)
	}
}

func verifySegments(t *testing.T, conf *TestConfig, p *config.PipelineConfig, res *livekit.EgressInfo) {
	// egress info
	require.Equal(t, res.Error == "", res.Status != livekit.EgressStatus_EGRESS_FAILED)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// segments info
	segments := res.GetSegments()
	require.NotEmpty(t, segments.PlaylistName)
	require.NotEmpty(t, segments.PlaylistLocation)
	require.Greater(t, segments.Size, int64(0))
	require.Greater(t, segments.Duration, int64(0))
	require.Equal(t, segments.SegmentCount, segments.Duration*time.Second/int64(p.SegmentDuration)+1)

	storedPlaylistPath := segments.PlaylistName
	localPlaylistPath := segments.PlaylistName

	// download from cloud storage
	if p.UploadConfig != nil {
		base := storedPlaylistPath[:len(storedPlaylistPath)-5]
		localPlaylistPath = fmt.Sprintf("%s/%s", conf.LocalOutputDirectory, storedPlaylistPath)
		download(t, p.UploadConfig, localPlaylistPath, storedPlaylistPath)
		download(t, p.UploadConfig, localPlaylistPath+".json", storedPlaylistPath+".json")
		for i := 0; i < int(segments.SegmentCount); i++ {
			cloudPath := fmt.Sprintf("%s_%05d.ts", base, i)
			localPath := fmt.Sprintf("%s/%s", conf.LocalOutputDirectory, cloudPath)
			download(t, p.UploadConfig, localPath, cloudPath)
		}
	}

	// verify
	verify(t, localPlaylistPath, p, res, ResultTypeSegments, conf.Muting, conf.sourceFramerate)
}

func verify(t *testing.T, input string, p *config.PipelineConfig, res *livekit.EgressInfo, resultType ResultType, withMuting bool, sourceFramerate float64) {
	info, err := ffprobe(input)
	require.NoError(t, err, "ffprobe error - input does not exist")

	switch p.OutputType {
	case types.OutputTypeRaw:
		require.Equal(t, 0, info.Format.ProbeScore)
	case types.OutputTypeIVF:
		require.Equal(t, 98, info.Format.ProbeScore)
	default:
		require.Equal(t, 100, info.Format.ProbeScore)
	}

	switch resultType {
	case ResultTypeFile:
		// size
		require.NotEqual(t, "0", info.Format.Size)

		// duration
		expected := float64(res.GetFile().Duration) / 1e9
		actual, err := strconv.ParseFloat(info.Format.Duration, 64)
		require.NoError(t, err)

		// file duration can be different from egress duration based on keyframes, muting, and latency
		delta := float64(builder.Latency) / 1e9
		switch p.Info.Request.(type) {
		case *livekit.EgressInfo_RoomComposite:
			require.InDelta(t, expected, actual, delta)

		case *livekit.EgressInfo_Track:
			if p.AudioEnabled {
				if withMuting {
					delta = 6
				}
				require.InDelta(t, expected, actual, delta)
			}
		}

	case ResultTypeSegments:
		// TODO: implement with Segments
	}

	// check stream info
	var hasAudio, hasVideo bool
	for _, stream := range info.Streams {
		switch stream.CodecType {
		case "audio":
			hasAudio = true

			// codec
			switch p.AudioCodec {
			case types.MimeTypeAAC:
				require.Equal(t, "aac", stream.CodecName)
				require.Equal(t, fmt.Sprint(p.AudioFrequency), stream.SampleRate)
				require.Equal(t, "stereo", stream.ChannelLayout)

			case types.MimeTypeOpus:
				require.Equal(t, "opus", stream.CodecName)
				require.Equal(t, "48000", stream.SampleRate)
				require.Equal(t, "stereo", stream.ChannelLayout)

			case types.MimeTypeRaw:
				require.Equal(t, "pcm_s16le", stream.CodecName)
				require.Equal(t, "48000", stream.SampleRate)
			}

			// channels
			require.Equal(t, 2, stream.Channels)

			// audio bitrate
			if p.OutputType == types.OutputTypeMP4 {
				bitrate, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.NotZero(t, bitrate)
			}

		case "video":
			hasVideo = true

			// codec and profile
			switch p.VideoCodec {
			case types.MimeTypeH264:
				require.Equal(t, "h264", stream.CodecName)

				switch p.VideoProfile {
				case types.ProfileBaseline:
					require.Equal(t, "Constrained Baseline", stream.Profile)
				case types.ProfileMain:
					require.Equal(t, "Main", stream.Profile)
				case types.ProfileHigh:
					require.Equal(t, "High", stream.Profile)
				}
			case types.MimeTypeVP8:
				require.Equal(t, "vp8", stream.CodecName)
			}

			switch p.OutputType {
			case types.OutputTypeIVF:
				require.Equal(t, "vp8", stream.CodecName)

			case types.OutputTypeMP4:
				// bitrate, not available for HLS or WebM
				bitrate, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.NotZero(t, bitrate)
				require.Less(t, int32(bitrate), p.VideoBitrate*1010)
				fallthrough

			case types.OutputTypeHLS:
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
				require.Less(t, n/d, float64(p.Framerate)*1.05)
				require.Greater(t, n/d, float64(sourceFramerate)*0.95)
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
