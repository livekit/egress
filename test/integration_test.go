//go:build integration
// +build integration

package test

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/pipeline"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
)

const (
	videoTestInput  = "https://www.youtube.com/watch?v=4cJpiOPKH14&t=25s"
	audioTestInput  = "https://www.youtube.com/watch?v=eAcFPtCyDYY&t=59s"
	staticTestInput = "https://www.livekit.io"
	defaultConfig   = `
log_level: info
api_key: fake_key
api_secret: fake_secret
ws_url: wss://fake-url.com
`
)

type testCase struct {
	name      string
	inputUrl  string
	audioOnly bool
	fileType  livekit.EncodedFileType
	options   *livekit.EncodingOptions
}

type sdkParams struct {
	audioTrackID string
	videoTrackID string
	url          string
}

func TestWebCompositeFile(t *testing.T) {
	conf := getTestConfig(t)

	for _, test := range []*testCase{
		{
			name:     "web-h264-baseline-mp4",
			inputUrl: videoTestInput,
			fileType: livekit.EncodedFileType_MP4,
			options: &livekit.EncodingOptions{
				AudioCodec:   livekit.AudioCodec_AAC,
				VideoCodec:   livekit.VideoCodec_H264_BASELINE,
				Height:       720,
				Width:        1280,
				VideoBitrate: 1500,
			},
		},
		{
			name:     "web-h264-main-mp4",
			inputUrl: staticTestInput,
			fileType: livekit.EncodedFileType_MP4,
		},
		{
			name:     "web-h264-high-mp4",
			inputUrl: videoTestInput,
			fileType: livekit.EncodedFileType_MP4,
			options: &livekit.EncodingOptions{
				Framerate:      60,
				AudioFrequency: 48000,
				VideoCodec:     livekit.VideoCodec_H264_HIGH,
				VideoBitrate:   6000,
			},
		},
		// {
		// 	name:     "web-vp8-webm",
		// 	inputUrl: videoTestInput,
		// 	fileType: livekit.EncodedFileType_WEBM,
		// 	options: &livekit.EncodingOptions{
		// 		AudioCodec: livekit.AudioCodec_OPUS,
		// 		VideoCodec: livekit.VideoCodec_VP8,
		// 	},
		// },
		// {
		// 	name:     "web-vp8-ogg",
		// 	inputUrl: videoTestInput,
		// 	fileType: livekit.EncodedFileType_OGG,
		// 	options: &livekit.EncodingOptions{
		// 		AudioCodec: livekit.AudioCodec_OPUS,
		// 		VideoCodec: livekit.VideoCodec_VP8,
		// 	},
		// },
		// {
		// 	name:     "web-vp9-webm",
		// 	inputUrl: videoTestInput,
		// 	fileType: livekit.EncodedFileType_WEBM,
		// 	options: &livekit.EncodingOptions{
		// 		AudioCodec: livekit.AudioCodec_OPUS,
		// 		VideoCodec: livekit.VideoCodec_VP9,
		// 	},
		// },
		// {
		// 	name:     "web-h265-main-mp4",
		// 	inputUrl: videoTestInput,
		// 	fileType: livekit.EncodedFileType_MP4,
		// 	options: &livekit.EncodingOptions{
		// 		AudioCodec: livekit.AudioCodec_AAC,
		// 		VideoCodec: livekit.VideoCodec_HEVC_MAIN,
		// 	},
		// },
		// {
		// 	name:     "web-h265-high-mp5",
		// 	inputUrl: videoTestInput,
		// 	fileType: livekit.EncodedFileType_MP4,
		// 	options: &livekit.EncodingOptions{
		// 		AudioCodec: livekit.AudioCodec_OPUS,
		// 		VideoCodec: livekit.VideoCodec_HEVC_HIGH,
		// 	},
		// },
		{
			name:      "web-opus-ogg",
			inputUrl:  audioTestInput,
			fileType:  livekit.EncodedFileType_OGG,
			audioOnly: true,
			options: &livekit.EncodingOptions{
				AudioCodec: livekit.AudioCodec_OPUS,
			},
		},
	} {
		if !t.Run(test.name, func(t *testing.T) { runWebCompositeFileTest(t, conf, test) }) {
			t.FailNow()
		}
	}
}

func TestWebCompositeStream(t *testing.T) {
	conf := getTestConfig(t)

	url := "rtmp://localhost:1935/live/stream1"

	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().Unix(),
		Request: &livekit.StartEgressRequest_WebComposite{
			WebComposite: &livekit.WebCompositeEgressRequest{
				RoomName:      "web-composite-stream",
				Layout:        "speaker-dark",
				CustomBaseUrl: videoTestInput,
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

	p, err := params.GetPipelineParams(conf, req)
	require.NoError(t, err)
	p.CustomInputURL = videoTestInput
	rec, err := pipeline.FromParams(conf, p)
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
	verifyStream(t, p, url)

	// add another, check both
	url2 := "rtmp://localhost:1935/live/stream2"
	require.NoError(t, rec.UpdateStream(&livekit.UpdateStreamRequest{
		EgressId:      req.EgressId,
		AddOutputUrls: []string{url2},
	}))
	verifyStream(t, p, url, url2)

	// remove first, check second
	require.NoError(t, rec.UpdateStream(&livekit.UpdateStreamRequest{
		EgressId:         req.EgressId,
		RemoveOutputUrls: []string{url},
	}))
	verifyStream(t, p, url2)

	// stop
	rec.Stop()
	res := <-resChan

	// check results
	require.Empty(t, res.Error)
	require.Len(t, res.GetStream().Info, 2)
	for _, info := range res.GetStream().Info {
		require.NotEmpty(t, info.Url)
		require.NotZero(t, info.StartedAt)
		require.NotZero(t, info.EndedAt)
	}
}

// func TestTrackCompositeFile(t *testing.T) {
// 	conf := getTestConfig(t)
//
// 	var audioTrackID, videoTrackID string
//
// 	p := &sdkParams{
// 		audioTrackID: audioTrackID,
// 		videoTrackID: videoTrackID,
// 	}
//
// 	for _, test := range []*testCase{} {
// 		if !t.Run(test.name, func(t *testing.T) {
// 			runTrackCompositeFileTest(t, conf, p, test)
// 		}) {
// 			t.FailNow()
// 		}
// 	}
// }

// func TestTrackEgress(t *testing.T) {}

func getTestConfig(t *testing.T) *config.Config {
	confString := defaultConfig
	confFile := os.Getenv("EGRESS_CONFIG_FILE")
	if confFile != "" {
		b, err := ioutil.ReadFile(confFile)
		if err == nil {
			confString = string(b)
		}
	}

	conf, err := config.NewConfig(confString)
	require.NoError(t, err)

	return conf
}

func runWebCompositeFileTest(t *testing.T, conf *config.Config, test *testCase) {
	filepath, filename := getFileInfo(conf, test, "web")

	roomName := os.Getenv("LIVEKIT_ROOM_NAME")
	if roomName == "" {
		roomName = "web-composite-file"
	}

	webRequest := &livekit.WebCompositeEgressRequest{
		RoomName:  roomName,
		Layout:    "speaker-dark",
		AudioOnly: test.audioOnly,
		Output: &livekit.WebCompositeEgressRequest_File{
			File: &livekit.EncodedFileOutput{
				FileType: test.fileType,
				Filepath: filepath,
			},
		},
	}

	if test.options != nil {
		webRequest.Options = &livekit.WebCompositeEgressRequest_Advanced{
			Advanced: test.options,
		}
	}

	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().UnixNano(),
		Request: &livekit.StartEgressRequest_WebComposite{
			WebComposite: webRequest,
		},
	}

	runFileTest(t, conf, test, req, filename)
}

func runTrackCompositeFileTest(t *testing.T, conf *config.Config, params *sdkParams, test *testCase) {
	filepath, filename := getFileInfo(conf, test, "track")

	roomName := os.Getenv("LIVEKIT_ROOM_NAME")
	if roomName == "" {
		roomName = "egress-integration"
	}

	trackRequest := &livekit.TrackCompositeEgressRequest{
		RoomName:     roomName,
		AudioTrackId: params.audioTrackID,
		VideoTrackId: params.videoTrackID,
		Output: &livekit.TrackCompositeEgressRequest_File{
			File: &livekit.EncodedFileOutput{
				FileType: test.fileType,
				Filepath: filepath,
			},
		},
	}

	if test.options != nil {
		trackRequest.Options = &livekit.TrackCompositeEgressRequest_Advanced{
			Advanced: test.options,
		}
	}

	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().UnixNano(),
		Request: &livekit.StartEgressRequest_TrackComposite{
			TrackComposite: trackRequest,
		},
	}

	runFileTest(t, conf, test, req, filename)
}

func getFileInfo(conf *config.Config, test *testCase, testType string) (string, string) {
	var name string
	if test.audioOnly {
		if test.options != nil && test.options.AudioCodec != livekit.AudioCodec_DEFAULT_AC {
			name = test.options.AudioCodec.String()
		} else {
			name = params.DefaultAudioCodecs[test.fileType.String()].String()
		}
	} else {
		if test.options != nil && test.options.VideoCodec != livekit.VideoCodec_DEFAULT_VC {
			name = test.options.VideoCodec.String()
		} else {
			name = params.DefaultVideoCodecs[test.fileType.String()].String()
		}
	}

	filename := fmt.Sprintf("%s-%s-%d.%s",
		testType, strings.ToLower(name), time.Now().Unix(), strings.ToLower(test.fileType.String()),
	)
	filepath := fmt.Sprintf("/out/%s", filename)

	if conf.FileUpload != nil {
		return filepath, filename
	}
	return filepath, filepath
}

func runFileTest(t *testing.T, conf *config.Config, test *testCase, req *livekit.StartEgressRequest, filename string) {
	p, err := params.GetPipelineParams(conf, req)
	require.NoError(t, err)

	if !strings.HasPrefix(conf.ApiKey, "API") || test.inputUrl == staticTestInput {
		p.CustomInputURL = test.inputUrl
	}

	rec, err := pipeline.FromParams(conf, p)
	require.NoError(t, err)

	// record for ~15s. Takes about 5s to start
	time.AfterFunc(time.Second*20, func() {
		rec.Stop()
	})
	res := rec.Run()

	// check results
	require.Empty(t, res.Error)
	fileRes := res.GetFile()
	require.NotNil(t, fileRes)
	require.NotZero(t, fileRes.StartedAt)
	require.NotZero(t, fileRes.EndedAt)
	require.NotEmpty(t, fileRes.Filename)
	require.NotEmpty(t, fileRes.Location)

	verify(t, filename, p, res, false, test.fileType)
}

func verifyStream(t *testing.T, p *params.Params, urls ...string) {
	for _, url := range urls {
		verify(t, url, p, nil, true, -1)
	}
}

func verify(t *testing.T, input string, p *params.Params, res *livekit.EgressInfo, isStream bool, fileType livekit.EncodedFileType) {
	info, err := ffprobe(input)
	require.NoError(t, err, "ffprobe error")
	require.Equal(t, 100, info.Format.ProbeScore)

	if !isStream {
		// size
		require.NotEqual(t, "0", info.Format.Size)

		// duration
		expected := time.Unix(0, res.GetFile().EndedAt).Sub(time.Unix(0, res.GetFile().StartedAt)).Seconds()
		actual, err := strconv.ParseFloat(info.Format.Duration, 64)
		require.NoError(t, err)
		require.InDelta(t, expected, actual, 1)
	}

	// check stream info
	var hasAudio, hasVideo bool
	for _, stream := range info.Streams {
		switch stream.CodecType {
		case "audio":
			hasAudio = true

			// codec
			switch p.AudioCodec {
			case livekit.AudioCodec_AAC:
				require.Equal(t, "aac", stream.CodecName)
				require.Equal(t, fmt.Sprint(p.AudioFrequency), stream.SampleRate)
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
				require.Less(t, int32(bitrate), p.AudioBitrate*1100)
			}
		case "video":
			hasVideo = true

			// encoding profile
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

			// bitrate
			if fileType == livekit.EncodedFileType_MP4 {
				bitrate, err := strconv.Atoi(stream.BitRate)
				require.NoError(t, err)
				require.NotZero(t, bitrate)
				require.Less(t, int32(bitrate), p.VideoBitrate*1010)
			}
		default:
			t.Fatalf("unrecognized stream type %s", stream.CodecType)
		}
	}

	require.Equal(t, p.AudioEnabled, hasAudio)
	require.Equal(t, p.VideoEnabled, hasVideo)
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
