// +build integration

package test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"testing"
	"time"

	livekit "github.com/livekit/protocol/proto"
	"github.com/stretchr/testify/require"

	"github.com/livekit/livekit-recorder/pkg/config"
	"github.com/livekit/livekit-recorder/pkg/recorder"
)

func TestRecorder(t *testing.T) {
	conf, err := config.NewConfig("")
	require.NoError(t, err)
	conf.ApiKey = "key"
	conf.ApiSecret = "secret"
	conf.WsUrl = "ws://localhost:7880"

	if !t.Run("template-test", func(t *testing.T) {
		runFileTest(t, conf)
	}) {
		t.FailNow()
	}

	if !t.Run("rtmp-test", func(t *testing.T) {
		runRtmpTest(t, conf)
	}) {
		t.FailNow()
	}
}

func runFileTest(t *testing.T, conf *config.Config) {
	filepath := "path/file-test.mp4"
	req := &livekit.StartRecordingRequest{
		Input: &livekit.StartRecordingRequest_Url{
			Url: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		},
		Output: &livekit.StartRecordingRequest_Filepath{
			Filepath: filepath,
		},
	}

	rec := recorder.NewRecorder(conf)
	require.NoError(t, rec.Validate(req))

	// record for 15s. Takes about 5s to start
	time.AfterFunc(time.Second*20, func() {
		rec.Stop()
	})

	res := rec.Run("room_test")
	verifyFile(t, req, res, filepath)
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

	rec := recorder.NewRecorder(conf)
	require.NoError(t, rec.Validate(req))
	resChan := make(chan *livekit.RecordingResult, 1)
	go func() {
		resChan <- rec.Run("rtmp_test")
	}()

	// wait for recorder to start
	time.Sleep(time.Second * 30)

	// check stream
	verifyRtmp(t, req, rtmpUrl)

	// add another, check both
	rtmpUrl2 := "rtmp://localhost:1935/stream2"
	require.NoError(t, rec.AddOutput(rtmpUrl2))
	verifyRtmp(t, req, rtmpUrl, rtmpUrl2)

	// remove first, check second
	require.NoError(t, rec.RemoveOutput(rtmpUrl))
	verifyRtmp(t, req, rtmpUrl2)

	// stop
	rec.Stop()
	res := <-resChan

	// check error
	require.Empty(t, res.Error)
	require.NotEqual(t, int64(0), res.Duration)
}

func verifyFile(t *testing.T, req *livekit.StartRecordingRequest, res *livekit.RecordingResult, filename string) {
	// check error
	require.Empty(t, res.Error)
	verify(t, req, res, filename, false)
}

func verifyRtmp(t *testing.T, req *livekit.StartRecordingRequest, urls ...string) {
	for _, url := range urls {
		verify(t, req, nil, url, true)
	}
}

func verify(t *testing.T, req *livekit.StartRecordingRequest, res *livekit.RecordingResult, input string, isStream bool) {
	info, err := ffprobe(input)
	require.NoError(t, err)

	// check format info
	if isStream {
		require.Equal(t, "flv", info.Format.FormatName)
	} else {
		require.NotEqual(t, 0, info.Format.Size)
		require.NotEqual(t, int64(0), res.Duration)
		compareInfo(t, int32(res.Duration), info.Format.Duration, 0.95)
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
				compareInfo(t, req.Options.AudioBitrate*1000, stream.BitRate, 0.9)
			}
		case "video":
			hasVideo = true
			require.Equal(t, "h264", stream.CodecName)
			require.Equal(t, req.Options.Width, stream.Width)
			require.Equal(t, req.Options.Height, stream.Height)
			require.Equal(t, fmt.Sprintf("%d/1", req.Options.Framerate), stream.RFrameRate)
			if !isStream {
				compareInfo(t, req.Options.VideoBitrate*1000, stream.BitRate, 0.9)
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

func compareInfo(t *testing.T, expected int32, actual string, threshold float64) {
	parsed, err := strconv.ParseFloat(actual, 64)
	require.NoError(t, err)

	opt := float64(expected)
	if parsed < opt {
		require.Greater(t, threshold, (parsed-opt)/parsed)
	} else {
		require.Greater(t, threshold, (opt-parsed)/opt)
	}
}
