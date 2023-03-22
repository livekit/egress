//go:build integration

package test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grafov/m3u8"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

const (
	maxRetries = 5
	minDelay   = time.Millisecond * 100
	maxDelay   = time.Second * 5
)

var (
	segmentTimeRegexp = regexp.MustCompile(`_(\d{14})(\d{3})\.ts`)
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
		Width        int32  `json:"width"`
		Height       int32  `json:"height"`
		RFrameRate   string `json:"r_frame_rate"`
		AvgFrameRate string `json:"avg_frame_rate"`
		BitRate      string `json:"bit_rate"`
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
	if fileRes == nil {
		require.Len(t, res.FileResults, 1)
		fileRes = res.FileResults[0]
	}

	require.NotEmpty(t, fileRes.Location)
	require.Greater(t, fileRes.Size, int64(0))
	require.Greater(t, fileRes.Duration, int64(0))

	storagePath := fileRes.Filename
	localPath := fileRes.Filename
	require.NotEmpty(t, storagePath)
	require.False(t, strings.Contains(storagePath, "{"))

	// download from cloud storage
	if uploadConfig := p.Outputs[types.EgressTypeFile].UploadConfig; uploadConfig != nil {
		localPath = fmt.Sprintf("%s/%s", conf.LocalOutputDirectory, storagePath)
		download(t, uploadConfig, localPath, storagePath)
		download(t, uploadConfig, localPath+".json", storagePath+".json")
	}

	// verify
	verify(t, localPath, p, res, types.EgressTypeFile, conf.Muting, conf.sourceFramerate)
}

func verifyStreams(t *testing.T, p *config.PipelineConfig, conf *TestConfig, urls ...string) {
	for _, url := range urls {
		verify(t, url, p, nil, types.EgressTypeStream, false, conf.sourceFramerate)
	}
}

func verifySegments(t *testing.T, conf *TestConfig, p *config.PipelineConfig, filenameSuffix livekit.SegmentedFileSuffix, res *livekit.EgressInfo) {
	// egress info
	require.Equal(t, res.Error == "", res.Status != livekit.EgressStatus_EGRESS_FAILED)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// segments info
	require.Len(t, res.GetSegmentResults(), 1)
	segments := res.GetSegmentResults()[0]

	require.NotEmpty(t, segments.PlaylistName)
	require.NotEmpty(t, segments.PlaylistLocation)
	require.Greater(t, segments.Size, int64(0))
	require.Greater(t, segments.Duration, int64(0))

	storedPlaylistPath := segments.PlaylistName
	localPlaylistPath := segments.PlaylistName

	verifyPlaylistProgramDateTime(t, filenameSuffix, localPlaylistPath)

	// download from cloud storage
	if uploadConfig := p.Outputs[types.EgressTypeSegments].UploadConfig; uploadConfig != nil {
		base := storedPlaylistPath[:len(storedPlaylistPath)-5]
		localPlaylistPath = fmt.Sprintf("%s/%s", conf.LocalOutputDirectory, storedPlaylistPath)
		download(t, uploadConfig, localPlaylistPath, storedPlaylistPath)
		download(t, uploadConfig, localPlaylistPath+".json", storedPlaylistPath+".json")
		for i := 0; i < int(segments.SegmentCount); i++ {
			cloudPath := fmt.Sprintf("%s_%05d.ts", base, i)
			localPath := fmt.Sprintf("%s/%s", conf.LocalOutputDirectory, cloudPath)
			download(t, uploadConfig, localPath, cloudPath)
		}
	}

	// verify
	verify(t, localPlaylistPath, p, res, types.EgressTypeSegments, conf.Muting, conf.sourceFramerate)
}

func verifyPlaylistProgramDateTime(t *testing.T, filenameSuffix livekit.SegmentedFileSuffix, localPlaylistPath string) {
	file, err := os.Open(localPlaylistPath)
	require.NoError(t, err)
	defer file.Close()

	pl, tp, err := m3u8.DecodeFrom(file, false)
	require.NoError(t, err)
	require.Equal(t, m3u8.MEDIA, tp)

	now := time.Now()

	for i, s := range pl.(*m3u8.MediaPlaylist).Segments[:pl.(*m3u8.MediaPlaylist).Count()] {
		const leeway = 50 * time.Millisecond

		// Make sure the program date time is current, ie not more than 2 min in the past
		require.InDelta(t, now.Unix(), s.ProgramDateTime.Unix(), 120)

		if filenameSuffix == livekit.SegmentedFileSuffix_TIMESTAMP {
			m := segmentTimeRegexp.FindStringSubmatch(s.URI)
			require.Equal(t, 3, len(m))

			tm, err := time.Parse("20060102150405", m[1])
			require.NoError(t, err)

			ms, err := strconv.Atoi(m[2])
			require.NoError(t, err)

			tm = tm.Add(time.Duration(ms) * time.Millisecond)

			require.InDelta(t, s.ProgramDateTime.UnixNano(), tm.UnixNano(), float64(time.Millisecond))
		}

		if uint(i) < pl.(*m3u8.MediaPlaylist).Count()-1 {
			nextSegmentStartDate := pl.(*m3u8.MediaPlaylist).Segments[i+1].ProgramDateTime

			dateDuration := nextSegmentStartDate.Sub(s.ProgramDateTime)
			require.InDelta(t, time.Duration(s.Duration*float64(time.Second)), dateDuration, float64(leeway))
		}
	}
}

func verify(t *testing.T, in string, p *config.PipelineConfig, res *livekit.EgressInfo, egressType types.EgressType, withMuting bool, sourceFramerate float64) {
	info, err := ffprobe(in)
	require.NoError(t, err, "input %s does not exist", in)

	switch p.Outputs[egressType].OutputType {
	case types.OutputTypeRaw:
		require.Equal(t, 0, info.Format.ProbeScore)
	case types.OutputTypeIVF:
		require.Equal(t, 98, info.Format.ProbeScore)
	default:
		require.Equal(t, 100, info.Format.ProbeScore)
	}

	switch egressType {
	case types.EgressTypeFile:
		// size
		require.NotEqual(t, "0", info.Format.Size)

		// duration
		fileRes := res.GetFile()
		if fileRes == nil {
			fileRes = res.FileResults[0]
		}
		expected := float64(fileRes.Duration) / 1e9
		actual, err := strconv.ParseFloat(info.Format.Duration, 64)
		require.NoError(t, err)

		// file duration can be different from egress duration based on keyframes, muting, and latency
		delta := 4.5
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

	case types.EgressTypeSegments:
		actual, err := strconv.ParseFloat(info.Format.Duration, 64)
		require.NoError(t, err)

		require.Len(t, res.GetSegmentResults(), 1)
		segments := res.GetSegmentResults()[0]
		expected := int64(math.Ceil(actual / float64(p.Outputs[egressType].SegmentDuration)))
		require.True(t, segments.SegmentCount == expected || segments.SegmentCount == expected-1)
	}

	// check stream info
	var hasAudio, hasVideo bool
	for _, stream := range info.Streams {
		switch stream.CodecType {
		case "audio":
			hasAudio = true

			// codec
			switch p.AudioOutCodec {
			case types.MimeTypeAAC:
				require.Equal(t, "aac", stream.CodecName)
				require.Equal(t, fmt.Sprint(p.AudioFrequency), stream.SampleRate)
				require.Equal(t, "stereo", stream.ChannelLayout)

			case types.MimeTypeOpus:
				require.Equal(t, "opus", stream.CodecName)
				require.Equal(t, "48000", stream.SampleRate)
				require.Equal(t, "stereo", stream.ChannelLayout)

			case types.MimeTypeRawAudio:
				require.Equal(t, "pcm_s16le", stream.CodecName)
				require.Equal(t, "48000", stream.SampleRate)
			}

			// channels
			require.Equal(t, 2, stream.Channels)

			// audio bitrate
			if p.Outputs[egressType].OutputType == types.OutputTypeMP4 {
				bitrate, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.NotZero(t, bitrate)
			}

		case "video":
			hasVideo = true

			// codec and profile
			switch p.VideoOutCodec {
			case types.MimeTypeH264:
				require.Equal(t, "h264", stream.CodecName)

				if p.VideoTranscoding {
					switch p.VideoProfile {
					case types.ProfileBaseline:
						require.Equal(t, "Constrained Baseline", stream.Profile)
					case types.ProfileMain:
						require.Equal(t, "Main", stream.Profile)
					case types.ProfileHigh:
						require.Equal(t, "High", stream.Profile)
					}
				}
			case types.MimeTypeVP8:
				require.Equal(t, "vp8", stream.CodecName)
			}

			switch p.Outputs[egressType].OutputType {
			case types.OutputTypeIVF:
				require.Equal(t, "vp8", stream.CodecName)

			case types.OutputTypeMP4:
				require.Equal(t, "h264", stream.CodecName)

				if p.VideoTranscoding {
					// bitrate, not available for HLS or WebM
					bitrate, err := strconv.Atoi(stream.BitRate)
					require.NoError(t, err)
					require.NotZero(t, bitrate)
					require.Less(t, int32(bitrate), p.VideoBitrate*1010)

					// framerate
					frac := strings.Split(stream.AvgFrameRate, "/")
					require.Len(t, frac, 2)
					n, err := strconv.ParseFloat(frac[0], 64)
					require.NoError(t, err)
					d, err := strconv.ParseFloat(frac[1], 64)
					require.NoError(t, err)
					require.NotZero(t, d)
					require.Less(t, n/d, float64(p.Framerate)*1.05)
					require.Greater(t, n/d, float64(sourceFramerate)*0.8)

				}
				fallthrough

			case types.OutputTypeHLS:
				require.Equal(t, "h264", stream.CodecName)

				if p.VideoTranscoding {
					// dimensions
					require.Equal(t, p.Width, stream.Width)
					require.Equal(t, p.Height, stream.Height)
				}
			}

		default:
			t.Fatalf("unrecognized stream type %s", stream.CodecType)
		}
	}

	if p.AudioEnabled {
		require.True(t, hasAudio)
		require.NotEmpty(t, p.AudioOutCodec)
	}

	if p.VideoEnabled {
		require.True(t, hasVideo)
		require.NotEmpty(t, p.VideoOutCodec)
	}
}
