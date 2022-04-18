//go:build integration
// +build integration

package test

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/pipeline"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
)

const (
	videoTestInput  = "https://www.youtube.com/watch?v=4cJpiOPKH14&t=25s"
	audioTestInput  = "https://www.youtube.com/watch?v=eAcFPtCyDYY&t=59s"
	audioTestInput2 = "https://www.youtube.com/watch?v=BlPbAq1dW3I&t=45s"
	staticTestInput = "https://www.livekit.io"
	defaultConfig   = `
log_level: error
api_key: fake_key
api_secret: fake_secret
ws_url: wss://fake-url.com
`
)

var (
	samples = map[string]string{
		"opus": "/out/sample/matrix-trailer.ogg",
		"vp8":  "/out/sample/matrix-trailer.ivf",
		"h264": "/out/sample/matrix-trailer.h264",
	}

	frameDurations = map[string]time.Duration{
		"vp8":  time.Microsecond * 41708,
		"h264": time.Microsecond * 41708,
	}
)

type testCase struct {
	name             string
	inputUrl         string
	forceCustomInput bool
	audioOnly        bool
	videoOnly        bool
	fileType         livekit.EncodedFileType
	options          *livekit.EncodingOptions
	filePrefix       string
}

type sdkParams struct {
	audioTrackID string
	videoTrackID string
	roomName     string
	url          string
}

func TestEgress(t *testing.T) {
	conf := getTestConfig(t)

	var room *lksdk.Room
	// var p *sdkParams

	if strings.HasPrefix(conf.ApiKey, "API") {
		roomName := os.Getenv("LIVEKIT_ROOM_NAME")
		if roomName == "" {
			roomName = "egress-integration"
		}

		var err error
		room, err = lksdk.ConnectToRoom(conf.WsUrl, lksdk.ConnectInfo{
			APIKey:              conf.ApiKey,
			APISecret:           conf.ApiSecret,
			RoomName:            roomName,
			ParticipantName:     "sample",
			ParticipantIdentity: fmt.Sprintf("sample-%d", rand.Intn(100)),
		})
		require.NoError(t, err)
		defer room.Disconnect()

		// p = publishSamplesToRoom(t, room, "opus", "vp8")
	}

	// t.Run("RoomCompositeFile", func(t *testing.T) {
	// 	testRoomCompositeFile(t, conf)
	// })
	//
	// t.Run("RoomCompositeStream", func(t *testing.T) {
	// 	testRoomCompositeStream(t, conf)
	// })

	if room == nil {
		return
	}

	// require.NoError(t, room.LocalParticipant.UnpublishTrack(p.audioTrackID))
	// require.NoError(t, room.LocalParticipant.UnpublishTrack(p.videoTrackID))

	t.Run("TrackComposite", func(t *testing.T) {
		testTrackComposite(t, conf, room)
	})
}

func getTestConfig(t *testing.T) *config.Config {
	confString := defaultConfig
	confFile := os.Getenv("EGRESS_CONFIG_FILE")
	if confFile != "/out/" {
		b, err := ioutil.ReadFile(confFile)
		if err == nil {
			confString = string(b)
		}
	}

	conf, err := config.NewConfig(confString)
	require.NoError(t, err)

	return conf
}

func publishSamplesToRoom(t *testing.T, room *lksdk.Room, audioCodec, videoCodec string) *sdkParams {
	p := &sdkParams{roomName: room.Name}

	publish := func(filename string, frameDuration time.Duration) string {
		var pub *lksdk.LocalTrackPublication
		opts := []lksdk.FileSampleProviderOption{
			lksdk.FileTrackWithOnWriteComplete(func() {
				if pub != nil {
					_ = room.LocalParticipant.UnpublishTrack(pub.SID())
				}
			}),
		}

		if frameDuration != 0 {
			opts = append(opts, lksdk.FileTrackWithFrameDuration(frameDuration))
		}

		track, err := lksdk.NewLocalFileTrack(filename, opts...)
		require.NoError(t, err)

		pub, err = room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: filename})
		require.NoError(t, err)

		return pub.SID()
	}

	if samples[audioCodec] != "" {
		p.audioTrackID = publish(samples[audioCodec], frameDurations[audioCodec])
	}

	if samples[videoCodec] != "" {
		p.videoTrackID = publish(samples[videoCodec], frameDurations[videoCodec])
	}

	return p
}

func getFileInfo(conf *config.Config, test *testCase, testType string) (string, string) {
	var filename string
	if test.filePrefix == "" {
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

		filename = fmt.Sprintf("%s-%s-%d.%s",
			testType, strings.ToLower(name), time.Now().Unix(), strings.ToLower(test.fileType.String()),
		)
	} else {
		filename = fmt.Sprintf("%s-%d.%s", test.filePrefix, time.Now().Unix(), strings.ToLower(test.fileType.String()))
	}

	filepath := fmt.Sprintf("/out/output/%s", filename)

	if conf.FileUpload != nil {
		return filepath, filename
	}

	return filepath, filepath
}

func runFileTest(t *testing.T, conf *config.Config, test *testCase, req *livekit.StartEgressRequest, filename string) {
	p, err := params.GetPipelineParams(conf, req)
	require.NoError(t, err)

	if !strings.HasPrefix(conf.ApiKey, "API") || test.forceCustomInput {
		p.CustomInputURL = test.inputUrl
	}

	rec, err := pipeline.FromParams(conf, p)
	require.NoError(t, err)

	// record for ~30s. Takes about 5s to start
	time.AfterFunc(time.Second*35, func() {
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
	require.NotEmpty(t, fileRes.Size)
	require.NotEmpty(t, fileRes.Location)

	verify(t, filename, p, res, false, test.fileType)
}
