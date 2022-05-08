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
	samples = map[params.MimeType]string{
		params.MimeTypeOpus: "/out/sample/matrix-trailer.ogg",
		params.MimeTypeH264: "/out/sample/matrix-trailer.h264",
		params.MimeTypeVP8:  "/out/sample/matrix-trailer.ivf",
	}

	frameDurations = map[params.MimeType]time.Duration{
		params.MimeTypeH264: time.Microsecond * 41708,
		params.MimeTypeVP8:  time.Microsecond * 41708,
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
	filename         string
	codec            params.MimeType
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
	var p *sdkParams

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

		p = publishSamplesToRoom(t, room, params.MimeTypeOpus, params.MimeTypeVP8, false)
	}

	if !t.Run("RoomCompositeFile", func(t *testing.T) {
		testRoomCompositeFile(t, conf)
	}) {
		t.FailNow()
	}

	if !t.Run("RoomCompositeStream", func(t *testing.T) {
		testRoomCompositeStream(t, conf)
	}) {
		t.FailNow()
	}

	if room == nil {
		return
	}

	require.NoError(t, room.LocalParticipant.UnpublishTrack(p.audioTrackID))
	require.NoError(t, room.LocalParticipant.UnpublishTrack(p.videoTrackID))

	if !t.Run("TrackComposite", func(t *testing.T) {
		testTrackComposite(t, conf, room)
	}) {
		t.FailNow()
	}

	if !t.Run("TrackFile", func(t *testing.T) {
		testTrackFile(t, conf, room)
	}) {
		t.FailNow()
	}

	if !t.Run("TrackWebsocket", func(t *testing.T) {
		testTrackWebsocket(t, conf, room)
	}) {
		t.FailNow()
	}
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

func publishSamplesToRoom(t *testing.T, room *lksdk.Room, audioCodec, videoCodec params.MimeType, withMuting bool) *sdkParams {
	p := &sdkParams{roomName: room.Name}

	publish := func(filename string, frameDuration time.Duration) string {
		var pub *lksdk.LocalTrackPublication
		done := make(chan struct{})
		opts := []lksdk.FileSampleProviderOption{
			lksdk.FileTrackWithOnWriteComplete(func() {
				close(done)
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

		if withMuting {
			go func() {
				muted := false
				for {
					select {
					case <-done:
						return
					default:
						pub.SetMuted(!muted)
						muted = !muted
						time.Sleep(time.Second * 5)
					}
				}
			}()
		}

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

func getFilePath(conf *config.Config, filename string) string {
	if conf.FileUpload != nil {
		return filename
	}

	return fmt.Sprintf("/out/output/%s", filename)
}

func runFileTest(t *testing.T, conf *config.Config, test *testCase, req *livekit.StartEgressRequest, filepath string) {
	p, err := params.GetPipelineParams(conf, req)
	require.NoError(t, err)

	if !strings.HasPrefix(conf.ApiKey, "API") || test.forceCustomInput {
		p.CustomInputURL = test.inputUrl
	}

	rec, err := pipeline.New(conf, p)
	require.NoError(t, err)

	// record for ~30s. Takes about 5s to start
	time.AfterFunc(time.Second*35, func() {
		rec.Stop()
	})
	res := rec.Run()

	// egress info
	require.Empty(t, res.Error)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// file info
	fileRes := res.GetFile()
	require.NotNil(t, fileRes)
	require.NotZero(t, fileRes.Duration)
	require.NotEmpty(t, fileRes.Filename)
	require.NotEmpty(t, fileRes.Size)
	require.NotEmpty(t, fileRes.Location)

	verify(t, filepath, p, res, false)
}
