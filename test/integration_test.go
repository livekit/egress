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

var confString = "log_level: debug"

func TestWebComposite(t *testing.T) {
	conf, err := config.NewConfig(confString)
	require.NoError(t, err)

	testInput := "https://www.youtube.com/watch?v=4cJpiOPKH14&t=25s"
	testInput2 := "https://www.youtube.com/watch?v=eAcFPtCyDYY&t=59s"
	staticInput := "https://www.livekit.io"

	for _, test := range []struct {
		name string
		f    func(t *testing.T)
	}{
		{
			name: "web-h264-baseline-mp4",
			f: func(t *testing.T) {
				testWebCompositeFile(t, conf, testInput, true,
					livekit.EncodedFileType_MP4,
					&livekit.EncodingOptions{
						AudioCodec:   livekit.AudioCodec_AAC,
						VideoCodec:   livekit.VideoCodec_H264_BASELINE,
						Height:       720,
						Width:        1280,
						VideoBitrate: 1500,
					})
			},
		},
		{
			name: "web-h264-main-mp4",
			f: func(t *testing.T) {
				testWebCompositeFile(t, conf, staticInput, true,
					livekit.EncodedFileType_MP4,
					nil,
				)
			},
		},
		{
			name: "web-h264-high-mp4",
			f: func(t *testing.T) {
				testWebCompositeFile(t, conf, testInput, true,
					livekit.EncodedFileType_MP4,
					&livekit.EncodingOptions{
						Framerate:      60,
						AudioFrequency: 48000,
						VideoCodec:     livekit.VideoCodec_H264_HIGH,
						VideoBitrate:   6000,
					})
			},
		},
		// {
		// 	name: "web-vp8-webm",
		// 	f: func(t *testing.T) {
		// 		testWebCompositeFile(t, conf, testInput, true,
		// 			livekit.EncodedFileType_WEBM,
		// 			&livekit.EncodingOptions{
		// 				AudioCodec: livekit.AudioCodec_OPUS,
		// 				VideoCodec: livekit.VideoCodec_VP8,
		// 			})
		// 	},
		// },
		// {
		// 	name: "web-vp8-ogg",
		// 	f: func(t *testing.T) {
		// 		testWebCompositeFile(t, conf, testInput, true,
		// 			livekit.EncodedFileType_OGG,
		// 			&livekit.EncodingOptions{
		// 				AudioCodec: livekit.AudioCodec_OPUS,
		// 				VideoCodec: livekit.VideoCodec_VP8,
		// 			})
		// 	},
		// },
		// {
		// 	name: "web-vp9-webm",
		// 	f: func(t *testing.T) {
		// 		testWebCompositeFile(t, conf, testInput, true,
		// 			livekit.EncodedFileType_WEBM,
		// 			&livekit.EncodingOptions{
		// 				AudioCodec: livekit.AudioCodec_OPUS,
		// 				VideoCodec: livekit.VideoCodec_VP9,
		// 			})
		// 	},
		// },
		// {
		// 	name: "web-h265-main-mp4",
		// 	f: func(t *testing.T) {
		// 		testWebCompositeFile(t, conf, testInput, true,
		// 			livekit.EncodedFileType_MP4,
		// 			&livekit.EncodingOptions{
		// 				AudioCodec: livekit.AudioCodec_AAC,
		// 				VideoCodec: livekit.VideoCodec_HEVC_MAIN,
		// 			})
		// 	},
		// },
		// {
		// 	name: "web-h265-high-mp5",
		// 	f: func(t *testing.T) {
		// 		testWebCompositeFile(t, conf, testInput, true,
		// 			livekit.EncodedFileType_MP4,
		// 			&livekit.EncodingOptions{
		// 				AudioCodec: livekit.AudioCodec_OPUS,
		// 				VideoCodec: livekit.VideoCodec_HEVC_HIGH,
		// 			})
		// 	},
		// },
		{
			name: "web-opus-ogg",
			f: func(t *testing.T) {
				testWebCompositeFile(t, conf, testInput2, false,
					livekit.EncodedFileType_OGG,
					&livekit.EncodingOptions{
						AudioCodec: livekit.AudioCodec_OPUS,
					},
				)
			},
		},
		{
			name: "web-h264-main-rtmp",
			f:    func(t *testing.T) { testWebCompositeStream(t, conf, testInput) },
		},
	} {
		if !t.Run(test.name, test.f) {
			t.FailNow()
		}
	}
}

func testWebCompositeFile(
	t *testing.T, conf *config.Config, inputUrl string, videoEnabled bool,
	fileType livekit.EncodedFileType, options *livekit.EncodingOptions,
) {
	videoCodec := livekit.VideoCodec_H264_MAIN
	if options != nil {
		videoCodec = options.VideoCodec
	}

	filepath := fmt.Sprintf("/out/web-%s-%d.%s",
		strings.ToLower(videoCodec.String()), time.Now().Unix(), strings.ToLower(fileType.String()))

	webRequest := &livekit.WebCompositeEgressRequest{
		RoomName:  "myroom",
		Layout:    "speaker-dark",
		AudioOnly: !videoEnabled,
		Output: &livekit.WebCompositeEgressRequest_File{
			File: &livekit.EncodedFileOutput{
				FileType: fileType,
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
		EgressId:  filepath,
		RequestId: filepath,
		SentAt:    time.Now().Unix(),
		Request: &livekit.StartEgressRequest_WebComposite{
			WebComposite: webRequest,
		},
	}

	params, err := config.GetPipelineParams(conf, req)
	require.NoError(t, err)
	params.CustomInputURL = inputUrl
	rec, err := pipeline.FromParams(conf, params)
	require.NoError(t, err)

	// record for ~15s. Takes about 5s to start
	time.AfterFunc(time.Second*20, func() {
		rec.Stop()
	})
	res := rec.Run()

	verifyFile(t, params, res, filepath, fileType)
}

func testWebCompositeStream(t *testing.T, conf *config.Config, inputUrl string) {
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
				Options: &livekit.WebCompositeEgressRequest_Advanced{
					Advanced: &livekit.EncodingOptions{
						AudioCodec: livekit.AudioCodec_AAC,
					},
				},
			},
		},
	}

	params, err := config.GetPipelineParams(conf, req)
	require.NoError(t, err)
	params.CustomInputURL = inputUrl
	rec, err := pipeline.FromParams(conf, params)
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
	verifyStream(t, params, url)

	// add another, check both
	url2 := "rtmp://localhost:1935/stream2"
	require.NoError(t, rec.UpdateStream(&livekit.UpdateStreamRequest{
		EgressId:      req.EgressId,
		AddOutputUrls: []string{url2},
	}))
	verifyStream(t, params, url, url2)

	// remove first, check second
	require.NoError(t, rec.UpdateStream(&livekit.UpdateStreamRequest{
		EgressId:         req.EgressId,
		RemoveOutputUrls: []string{url},
	}))
	verifyStream(t, params, url2)

	// stop
	rec.Stop()
	res := <-resChan

	// check error
	require.Empty(t, res.Error)
	require.NotZero(t, res.EndedAt)
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

func verifyFile(t *testing.T, params *config.Params, res *livekit.EgressInfo, filename string, fileType livekit.EncodedFileType) {
	require.Empty(t, res.Error)
	verify(t, filename, params, res, false, fileType)
}

func verifyStream(t *testing.T, params *config.Params, urls ...string) {
	for _, url := range urls {
		verify(t, url, params, nil, true, -1)
	}
}

func verify(t *testing.T, input string, params *config.Params, res *livekit.EgressInfo, isStream bool, fileType livekit.EncodedFileType) {
	info, err := ffprobe(input)
	require.NoError(t, err, "ffprobe error")

	if isStream {
		require.Equal(t, "flv", info.Format.FormatName)
	} else {
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

	if params.AudioEnabled && params.VideoEnabled {
		require.Len(t, info.Streams, 2)
	} else {
		require.Len(t, info.Streams, 1)
	}

	// check stream info
	var hasAudio, hasVideo bool
	for _, stream := range info.Streams {
		switch stream.CodecType {
		case "audio":
			hasAudio = true

			// codec
			switch params.AudioCodec {
			case livekit.AudioCodec_AAC:
				require.Equal(t, "aac", stream.CodecName)
				require.Equal(t, fmt.Sprint(params.AudioFrequency), stream.SampleRate)
			case livekit.AudioCodec_OPUS:
				require.Equal(t, "opus", stream.CodecName)
				require.Equal(t, "48000", stream.SampleRate)
			}

			// channels
			require.Equal(t, 2, stream.Channels)
			require.Equal(t, "stereo", stream.ChannelLayout)

			// audio bitrate
			if fileType == livekit.EncodedFileType_MP4 {
				bitrate, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.NotZero(t, bitrate)
				require.Less(t, int32(bitrate), params.AudioBitrate*1100)
			}
		case "video":
			hasVideo = true

			// encoding profile
			switch params.VideoCodec {
			case livekit.VideoCodec_H264_BASELINE:
				require.Equal(t, "h264", stream.CodecName)
				require.Equal(t, "Constrained Baseline", stream.Profile)
			case livekit.VideoCodec_H264_MAIN:
				require.Equal(t, "h264", stream.CodecName)
				require.Equal(t, "Main", stream.Profile)
			case livekit.VideoCodec_H264_HIGH:
				require.Equal(t, "h264", stream.CodecName)
				require.Equal(t, "High", stream.Profile)
				// case livekit.VideoCodec_VP8:
				// 	require.Equal(t, "vp8", stream.CodecName)
				// case livekit.VideoCodec_VP9:
				// 	require.Equal(t, "vp9", stream.CodecName)
				// case livekit.VideoCodec_HEVC_MAIN:
				// 	require.Equal(t, "hevc", stream.CodecName)
				// 	require.Equal(t, "Main", stream.Profile)
				// case livekit.VideoCodec_HEVC_HIGH:
				// 	require.Equal(t, "hevc", stream.CodecName)
				// 	require.Equal(t, "Main", stream.Profile)
			}

			// dimensions
			require.Equal(t, params.Width, stream.Width)
			require.Equal(t, params.Height, stream.Height)

			// framerate
			frac := strings.Split(stream.RFrameRate, "/")
			require.Len(t, frac, 2)
			n, err := strconv.ParseFloat(frac[0], 64)
			require.NoError(t, err)
			d, err := strconv.ParseFloat(frac[1], 64)
			require.NoError(t, err)
			require.Greater(t, n/d, float64(params.Framerate)*0.95)

			// bitrate
			if fileType == livekit.EncodedFileType_MP4 {
				bitrate, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.NotZero(t, bitrate)
				require.Less(t, int32(bitrate), params.VideoBitrate*1000)
			}
		default:
			t.Fatalf("unrecognized stream type %s", stream.CodecType)
		}
	}

	if params.AudioEnabled {
		require.True(t, hasAudio)
	}
	if params.VideoEnabled {
		require.True(t, hasVideo)
	}
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
