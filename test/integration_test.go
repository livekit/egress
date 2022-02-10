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

	for _, test := range []struct {
		name string
		f    func(t *testing.T)
	}{
		{
			name: "file-default",
			f: func(t *testing.T) {
				testRecording(t, conf, nil, "file-defaults", false)
			},
		},
		{
			name: "file-baseline",
			f: func(t *testing.T) {
				testRecording(t, conf, &livekit.RecordingOptions{
					Height:       720,
					Width:        1280,
					VideoBitrate: 1500,
					Profile:      config.ProfileBaseline,
				}, "file-baseline", false)
			},
		},
		{
			name: "file-high",
			f: func(t *testing.T) {
				testRecording(t, conf, &livekit.RecordingOptions{
					Framerate:      60,
					AudioFrequency: 48000,
					VideoBitrate:   6000,
					Profile:        config.ProfileHigh,
				}, "file-high", false)
			},
		},
		{
			name: "file-static",
			f: func(t *testing.T) {
				testRecording(t, conf, nil, "file-static", true)
			},
		},
		{
			name: "stream-default",
			f:    func(t *testing.T) { testStream(t, conf) },
		},
	} {
		if !t.Run(test.name, test.f) {
			t.FailNow()
		}
	}
}

func testRecording(t *testing.T,
	conf *config.Config, options *livekit.RecordingOptions,
	name string, isStatic bool,
) {
	filepath := fmt.Sprintf("/out/%s-%d.mp4", name, time.Now().Unix())

	var url string
	if isStatic {
		url = "https://www.livekit.io"
	} else {
		url = "https://www.youtube.com/watch?v=4cJpiOPKH14&t=30s"
	}

	req := &livekit.StartRecordingRequest{
		Input:   &livekit.StartRecordingRequest_Url{Url: url},
		Output:  &livekit.StartRecordingRequest_Filepath{Filepath: filepath},
		Options: options,
	}

	rec := recorder.NewRecorder(conf, name)
	defer func() {
		rec.Close()
		time.Sleep(time.Millisecond * 100)
	}()

	require.NoError(t, rec.Validate(req))

	// record for ~15s. Takes about 5s to start
	time.AfterFunc(time.Second*20, func() {
		rec.Stop()
	})
	res := rec.Run()

	verifyFile(t, req, res, filepath, isStatic)
}

func testStream(t *testing.T, conf *config.Config) {
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
	verifyStream(t, req, rtmpUrl)

	// add another, check both
	rtmpUrl2 := "rtmp://localhost:1935/stream2"
	require.NoError(t, rec.AddOutput(rtmpUrl2))
	verifyStream(t, req, rtmpUrl, rtmpUrl2)

	// remove first, check second
	require.NoError(t, rec.RemoveOutput(rtmpUrl))
	verifyStream(t, req, rtmpUrl2)

	// stop
	rec.Stop()
	res := <-resChan

	// check error
	require.Empty(t, res.Error)
	require.Len(t, res.Rtmp, 2)
	require.NotEqual(t, int64(0), res.Rtmp[0].Duration)
	require.NotEqual(t, int64(0), res.Rtmp[1].Duration)
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

func verifyFile(t *testing.T, req *livekit.StartRecordingRequest, res *livekit.RecordingInfo, filename string, isStatic bool) {
	// check error
	require.Empty(t, res.Error)

	// check file
	verify(t, req, res, filename, false, isStatic)
}

func verifyStream(t *testing.T, req *livekit.StartRecordingRequest, urls ...string) {
	for _, url := range urls {
		// check stream
		verify(t, req, nil, url, true, false)
	}
}

func verify(t *testing.T, req *livekit.StartRecordingRequest, res *livekit.RecordingInfo, input string, isStream, isStatic bool) {
	info, err := ffprobe(input)
	require.NoError(t, err)

	requireInRange := func(actual string, min, max float64) {
		v, err := strconv.ParseFloat(actual, 64)
		require.NoError(t, err)
		require.True(t, min < v && v < max, "min: %v, max: %v, actual: %v", min, max, v)
	}

	// check format info
	if isStream {
		require.Equal(t, "flv", info.Format.FormatName)
	} else {
		require.NotEqual(t, 0, info.Format.Size)
		require.NotNil(t, res.File)
		require.NotEqual(t, int64(0), res.File.Duration)
		requireInRange(info.Format.Duration, float64(res.File.Duration)-1.5, float64(res.File.Duration)+1.5)
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

			// stream info
			require.Equal(t, "aac", stream.CodecName)
			require.Equal(t, 2, stream.Channels)
			require.Equal(t, "stereo", stream.ChannelLayout)
			require.Equal(t, fmt.Sprint(req.Options.AudioFrequency), stream.SampleRate)

			// bitrate
			if !isStream {
				requireInRange(stream.BitRate, 0, float64(req.Options.AudioBitrate*1150))
			}
		case "video":
			hasVideo = true
			require.Equal(t, "h264", stream.CodecName)

			// encoding profile
			switch req.Options.Profile {
			case config.ProfileBaseline:
				require.Equal(t, "Constrained Baseline", stream.Profile)
			case config.ProfileMain:
				require.Equal(t, "Main", stream.Profile)
			case config.ProfileHigh:
				require.Equal(t, "High", stream.Profile)
			}

			// dimensions
			require.Equal(t, req.Options.Width, stream.Width)
			require.Equal(t, req.Options.Height, stream.Height)

			// framerate
			framerate := strings.Split(stream.RFrameRate, "/")
			require.Len(t, framerate, 2)
			num, err := strconv.Atoi(framerate[0])
			require.NoError(t, err)
			den, err := strconv.Atoi(framerate[1])
			require.NoError(t, err)
			require.Greater(t, (float32(num)/float32(den))/float32(req.Options.Framerate), float32(0.95))

			// bitrate
			if !isStream {
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
