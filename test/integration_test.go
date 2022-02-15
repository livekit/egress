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

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/pipeline"
)

var confString = `
log_level: debug
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
			name: "web-h264-baseline",
			f: func(t *testing.T) {
				testWebCompositeFile(t, "web-h264-baseline", conf, &livekit.EncodingOptions{
					Height:       720,
					Width:        1280,
					VideoCodec:   livekit.VideoCodec_H264_BASELINE,
					VideoBitrate: 1500,
				}, false)
			},
		},
		{
			name: "web-h264-main",
			f: func(t *testing.T) {
				testWebCompositeFile(t, "web-h264-main", conf, nil, false)
			},
		},
		{
			name: "web-h264-high",
			f: func(t *testing.T) {
				testWebCompositeFile(t, "web-h264-high", conf, &livekit.EncodingOptions{
					Framerate:      60,
					AudioFrequency: 48000,
					VideoCodec:     livekit.VideoCodec_H264_HIGH,
					VideoBitrate:   6000,
				}, false)
			},
		},
		{
			name: "web-static-video",
			f: func(t *testing.T) {
				testWebCompositeFile(t, "web-static-video", conf, nil, true)
			},
		},
		{
			name: "stream-h264-main",
			f:    func(t *testing.T) { testWebCompositeStream(t, conf) },
		},
	} {
		if !t.Run(test.name, test.f) {
			t.FailNow()
		}
	}
}

func testWebCompositeFile(t *testing.T, name string, conf *config.Config, options *livekit.EncodingOptions, static bool) {
	filepath := fmt.Sprintf("/out/%s-%d.mp4", name, time.Now().Unix())

	webRequest := &livekit.WebCompositeEgressRequest{
		RoomName: "myroom",
		Layout:   "speaker-dark",
		Output: &livekit.WebCompositeEgressRequest_File{
			File: &livekit.EncodedFileOutput{
				FileType: livekit.EncodedFileType_MP4,
				HttpUrl:  filepath,
			},
		},
	}

	if options != nil {
		webRequest.Options = &livekit.WebCompositeEgressRequest_Advanced{
			Advanced: options,
		}
	}

	req := &livekit.StartEgressRequest{
		EgressId:  name,
		RequestId: name,
		SentAt:    time.Now().Unix(),
		Request: &livekit.StartEgressRequest_WebComposite{
			WebComposite: webRequest,
		},
	}

	opts := getTestOpts(conf, req, static)
	rec, err := pipeline.New(conf, req, opts)
	require.NoError(t, err)

	// record for ~15s. Takes about 5s to start
	time.AfterFunc(time.Second*20, func() {
		rec.Stop()
	})
	res := rec.Run()

	verifyFile(t, conf.GetRecordingOptions(req), res, filepath)
}

func testWebCompositeStream(t *testing.T, conf *config.Config) {
	url := "rtmp://localhost:1935/stream1"

	req := &livekit.StartEgressRequest{
		EgressId:  "web-composite-stream",
		RequestId: "web-composite-stream",
		SentAt:    time.Now().Unix(),
		Request: &livekit.StartEgressRequest_WebComposite{
			WebComposite: &livekit.WebCompositeEgressRequest{
				RoomName:      "myroom",
				Layout:        "speaker-dark",
				CustomBaseUrl: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
				Output: &livekit.WebCompositeEgressRequest_Stream{
					Stream: &livekit.StreamOutput{
						Protocol: livekit.StreamProtocol_RTMP,
						Urls:     []string{url},
					},
				},
			},
		},
	}

	opts := getTestOpts(conf, req, false)
	rec, err := pipeline.New(conf, req, opts)
	require.NoError(t, err)

	defer func() {
		rec.Stop()
		time.Sleep(time.Millisecond * 100)
	}()

	resChan := make(chan *livekit.EgressInfo, 1)
	go func() {
		resChan <- rec.Run()
	}()

	// wait for recorder to start
	time.Sleep(time.Second * 30)

	// check stream
	verifyStream(t, opts, url)

	// add another, check both
	url2 := "rtmp://localhost:1935/stream2"
	require.NoError(t, rec.UpdateStream(&livekit.UpdateStreamRequest{
		EgressId:      req.EgressId,
		AddOutputUrls: []string{url2},
	}))
	verifyStream(t, opts, url, url2)

	// remove first, check second
	require.NoError(t, rec.UpdateStream(&livekit.UpdateStreamRequest{
		EgressId:         req.EgressId,
		RemoveOutputUrls: []string{url},
	}))
	verifyStream(t, opts, url2)

	// stop
	rec.Stop()
	res := <-resChan

	// check error
	require.Empty(t, res.Error)
	require.NotZero(t, res.EndedAt)
}

func getTestOpts(conf *config.Config, req *livekit.StartEgressRequest, static bool) *config.RecordingOptions {
	opts := conf.GetRecordingOptions(req)
	if static {
		opts.CustomInputURL = "https://www.livekit.io"
	} else {
		opts.CustomInputURL = "https://www.youtube.com/watch?v=4cJpiOPKH14&t=25s"
	}
	return opts
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

func verifyFile(t *testing.T, opts *config.RecordingOptions, res *livekit.EgressInfo, filename string) {
	require.Empty(t, res.Error)
	verify(t, filename, opts, res, false)
}

func verifyStream(t *testing.T, opts *config.RecordingOptions, urls ...string) {
	for _, url := range urls {
		verify(t, url, opts, nil, true)
	}
}

func verify(t *testing.T, input string, opts *config.RecordingOptions, res *livekit.EgressInfo, isStream bool) {
	info, err := ffprobe(input)
	require.NoError(t, err, "ffprobe error")

	if isStream {
		require.Equal(t, "flv", info.Format.FormatName)
	} else {
		require.Equal(t, "x264", info.Format.Tags.Encoder)
		require.NotEqual(t, "0", info.Format.Size)
		require.NotZero(t, res.StartedAt)
		require.NotZero(t, res.EndedAt)

		// duration
		expected := time.Unix(0, res.EndedAt).Sub(time.Unix(0, res.StartedAt)).Seconds()
		actual, err := strconv.ParseFloat(info.Format.Duration, 64)
		require.NoError(t, err)
		require.InDelta(t, expected, actual, 1)
	}
	require.Equal(t, 100, info.Format.ProbeScore)
	require.Len(t, info.Streams, 2)

	// check stream info
	var hasAudio, hasVideo bool
	for _, stream := range info.Streams {
		switch stream.CodecType {
		case "audio":
			hasAudio = true

			switch opts.AudioCodec {
			case livekit.AudioCodec_AAC:
				require.Equal(t, "aac", stream.CodecName)
			case livekit.AudioCodec_OPUS:
				t.FailNow()
			}

			require.Equal(t, 2, stream.Channels)
			require.Equal(t, "stereo", stream.ChannelLayout)
			require.Equal(t, fmt.Sprint(opts.AudioFrequency), stream.SampleRate)

			// audio bitrate
			if !isStream {
				bitrate, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.NotZero(t, bitrate)
				require.Less(t, int32(bitrate), opts.AudioBitrate*1100)
			}
		case "video":
			hasVideo = true

			// encoding profile
			switch opts.VideoCodec {
			case livekit.VideoCodec_H264_BASELINE:
				require.Equal(t, "h264", stream.CodecName)
				require.Equal(t, "Constrained Baseline", stream.Profile)
			case livekit.VideoCodec_H264_MAIN:
				require.Equal(t, "h264", stream.CodecName)
				require.Equal(t, "Main", stream.Profile)
			case livekit.VideoCodec_H264_HIGH:
				require.Equal(t, "h264", stream.CodecName)
				require.Equal(t, "High", stream.Profile)
			case livekit.VideoCodec_VP8:
				t.FailNow()
			case livekit.VideoCodec_VP9:
				t.FailNow()
			case livekit.VideoCodec_HEVC_MAIN:
				t.FailNow()
			case livekit.VideoCodec_HEVC_HIGH:
				t.FailNow()
			}

			// dimensions
			require.Equal(t, opts.Width, stream.Width)
			require.Equal(t, opts.Height, stream.Height)

			// framerate
			framerate := strings.Split(stream.RFrameRate, "/")
			require.Len(t, framerate, 2)
			num, err := strconv.ParseFloat(framerate[0], 64)
			require.NoError(t, err)
			den, err := strconv.ParseFloat(framerate[1], 64)
			require.NoError(t, err)
			require.InDelta(t, float64(opts.Framerate), num/den, 1.0)

			// bitrate
			if !isStream {
				bitrate, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.NotZero(t, bitrate)
				require.Less(t, int32(bitrate), opts.VideoBitrate*1000)
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
