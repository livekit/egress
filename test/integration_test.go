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
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"

	"github.com/livekit/livekit-recorder/pkg/config"
	"github.com/livekit/livekit-recorder/pkg/recorder"
)

var confString = `
log_level: info
api_key: key
api_secret: secret
ws_url: ws://localhost:7880`

func TestRecorder(t *testing.T) {
	conf, err := config.NewConfig(confString)
	require.NoError(t, err)

	if !t.Run("file-test-defaults", func(t *testing.T) {
		runFileTest(t, conf, nil)
	}) {
		t.FailNow()
	}

	if !t.Run("file-test-baseline", func(t *testing.T) {
		runFileTest(t, conf, &livekit.RecordingOptions{
			Height:       720,
			Width:        1280,
			VideoBitrate: 1500,
			Profile:      config.ProfileBaseline,
		})
	}) {
		t.FailNow()
	}

	if !t.Run("file-test-high", func(t *testing.T) {
		runFileTest(t, conf, &livekit.RecordingOptions{
			Framerate:      60,
			AudioFrequency: 48000,
			VideoBitrate:   6000,
			Profile:        config.ProfileHigh,
		})
	}) {
		t.FailNow()
	}

	if !t.Run("rtmp-test", func(t *testing.T) {
		runRtmpTest(t, conf)
	}) {
		t.FailNow()
	}
}

func runFileTest(t *testing.T, conf *config.Config, options *livekit.RecordingOptions) {
	filepath := fmt.Sprintf("path/file-test-%d.mp4", time.Now().Unix())
	req := &livekit.StartRecordingRequest{
		Input: &livekit.StartRecordingRequest_Url{
			Url: "https://www.youtube.com/watch?v=4cJpiOPKH14&t=30s",
		},
		Output: &livekit.StartRecordingRequest_Filepath{
			Filepath: filepath,
		},
		Options: options,
	}

	rec := recorder.NewRecorder(conf, "file_test")
	defer func() {
		rec.Close()
		time.Sleep(time.Millisecond * 100)
	}()

	require.NoError(t, rec.Validate(req))

	// record for 15s. Takes about 5s to start
	time.AfterFunc(time.Second*20, func() {
		rec.Stop()
	})
	res := rec.Run()

	verifyFileResult(t, req, res, filepath)
}

func runRtmpTest(t *testing.T, conf *config.Config) {
	rtmpUrl := "rtmp://localhost:1935/stream1"
	req := &livekit.StartRecordingRequest{
		Input: &livekit.StartRecordingRequest_Url{
			Url: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		},
		Output: &livekit.StartRecordingRequest_Rtmp{
			Rtmp: &livekit.RtmpOutput{
				Urls: []string{rtmpUrl},
			},
		},
	}

	rec := recorder.NewRecorder(conf, "rtmp_test")
	defer func() {
		rec.Close()
		time.Sleep(time.Millisecond * 100)
	}()

	require.NoError(t, rec.Validate(req))
	resChan := make(chan *livekit.RecordingInfo, 1)
	go func() {
		resChan <- rec.Run()
	}()

	// wait for recorder to start
	time.Sleep(time.Second * 30)

	// check stream
	verifyRtmpResult(t, req, rtmpUrl)

	// add another, check both
	rtmpUrl2 := "rtmp://localhost:1935/stream2"
	require.NoError(t, rec.AddOutput(rtmpUrl2))
	verifyRtmpResult(t, req, rtmpUrl, rtmpUrl2)

	// remove first, check second
	require.NoError(t, rec.RemoveOutput(rtmpUrl))
	verifyRtmpResult(t, req, rtmpUrl2)

	// stop
	rec.Stop()
	res := <-resChan

	// check error
	require.Empty(t, res.Error)
	require.Len(t, res.Rtmp, 2)
	require.NotEqual(t, int64(0), res.Rtmp[0].Duration)
	require.NotEqual(t, int64(0), res.Rtmp[1].Duration)
}

func verifyFileResult(t *testing.T, req *livekit.StartRecordingRequest, res *livekit.RecordingInfo, filename string) {
	// check error
	require.Empty(t, res.Error)
	verify(t, req, res, filename, false)
}

func verifyRtmpResult(t *testing.T, req *livekit.StartRecordingRequest, urls ...string) {
	for _, url := range urls {
		verify(t, req, nil, url, true)
	}
}

func verify(t *testing.T, req *livekit.StartRecordingRequest, res *livekit.RecordingInfo, input string, isStream bool) {
	info, err := ffprobe(input)
	require.NoError(t, err)

	requireInRange := func(actual string, min, max float64) {
		v, err := strconv.ParseFloat(actual, 64)
		require.NoError(t, err)
		require.True(t, min <= v && v <= max, "min: %v, max: %v, actual: %v", min, max, v)
	}

	// check format info
	if isStream {
		require.Equal(t, "flv", info.Format.FormatName)
	} else {
		require.NotEqual(t, 0, info.Format.Size)
		require.NotNil(t, res.File)
		require.NotEqual(t, int64(0), res.File.Duration)
		requireInRange(info.Format.Duration, float64(res.File.Duration-2), float64(res.File.Duration+2))
		require.Equal(t, "x264", info.Format.Tags.Encoder)
	}
	require.Equal(t, 100, info.Format.ProbeScore)
	require.Len(t, info.Streams, 2)

	// check stream info
	var hasAudio, hasVideo bool
	for _, stream := range info.Streams {
		switch stream.CodecType {
		case "audio":
			hasAudio = true
			require.Equal(t, "aac", stream.CodecName)
			require.Equal(t, 2, stream.Channels)
			require.Equal(t, "stereo", stream.ChannelLayout)
			require.Equal(t, fmt.Sprint(req.Options.AudioFrequency), stream.SampleRate)
			if !isStream {
				// audio bitrate
				expected := float64(req.Options.AudioBitrate * 1000)
				requireInRange(stream.BitRate, expected*0.95, expected*1.15)
			}
		case "video":
			hasVideo = true
			require.Equal(t, "h264", stream.CodecName)

			// profile
			switch req.Options.Profile {
			case config.ProfileBaseline:
				require.Equal(t, "Constrained Baseline", stream.Profile)
			case config.ProfileMain:
				require.Equal(t, "Main", stream.Profile)
			case config.ProfileHigh:
				require.Equal(t, "High", stream.Profile)
			}

			// dims
			require.Equal(t, req.Options.Width, stream.Width)
			require.Equal(t, req.Options.Height, stream.Height)

			// framerate value can be something like 359/12 or 30000/1001 instead of 30/1
			framerate := strings.Split(stream.RFrameRate, "/")
			require.Len(t, framerate, 2)
			num, err := strconv.Atoi(framerate[0])
			require.NoError(t, err)
			den, err := strconv.Atoi(framerate[1])
			require.NoError(t, err)
			require.True(t, (float32(num)/float32(den))/float32(req.Options.Framerate) > 0.95)

			if !isStream {
				// bitrate varies greatly
				br, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.NotZero(t, br)
			}
		default:
			t.Fatalf("unrecognized stream type %s", stream.CodecType)
		}
	}
	require.True(t, hasAudio && hasVideo)
}

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
