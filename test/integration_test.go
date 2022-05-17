//go:build integration
// +build integration

package test

import (
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/egress/pkg/pipeline/params"
)

const (
	videoTestInput  = "https://www.youtube.com/watch?v=4cJpiOPKH14&t=25s"
	audioTestInput  = "https://www.youtube.com/watch?v=eAcFPtCyDYY&t=59s"
	audioTestInput2 = "https://www.youtube.com/watch?v=BlPbAq1dW3I&t=45s"
	staticTestInput = "https://www.livekit.io"
	muteDuration    = time.Second * 10
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
	output           params.OutputType
}

type sdkParams struct {
	audioTrackID string
	videoTrackID string
	roomName     string
}

func TestEgress(t *testing.T) {
	conf := getTestConfig(t)

	var room *lksdk.Room
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
	}

	if !conf.TrackCompositeOnly && !conf.TrackOnly {
		if !t.Run("RoomComposite", func(t *testing.T) {
			testRoomComposite(t, conf, room)
		}) {
			t.FailNow()
		}
	}

	if room == nil {
		return
	}

	if !conf.RoomOnly && !conf.TrackOnly {
		if !t.Run("TrackComposite", func(t *testing.T) {
			testTrackComposite(t, conf, room)
		}) {
			t.FailNow()
		}
	}

	if !conf.RoomOnly && !conf.TrackCompositeOnly {
		if !t.Run("Track", func(t *testing.T) {
			testTrack(t, conf, room)
		}) {
			t.FailNow()
		}
	}
}

func publishSampleToRoom(t *testing.T, room *lksdk.Room, codec params.MimeType, withMuting bool) string {
	filename := samples[codec]
	frameDuration := frameDurations[codec]

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
			time.Sleep(muteDuration)
			for {
				select {
				case <-done:
					return
				default:
					pub.SetMuted(!muted)
					muted = !muted
					time.Sleep(muteDuration)
				}
			}
		}()
	}

	return pub.SID()
}

func getFilePath(conf *config.Config, filename string) string {
	if conf.FileUpload != nil {
		return filename
	}

	return fmt.Sprintf("/out/output/%s", filename)
}

func runFileTest(t *testing.T, conf *testConfig, test *testCase, req *livekit.StartEgressRequest, filepath string) {
	p, err := params.GetPipelineParams(conf.Config, req)
	require.NoError(t, err)

	if !strings.HasPrefix(conf.ApiKey, "API") || test.forceCustomInput {
		p.CustomInputURL = test.inputUrl
	}

	rec, err := pipeline.New(conf.Config, p)
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

	verify(t, filepath, p, res, false, conf.WithMuting)
}
