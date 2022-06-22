//go:build integration
// +build integration

package test

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
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
	streamUrl1      = "rtmp://localhost:1935/live/stream1"
	streamUrl2      = "rtmp://localhost:1935/live/stream2"
	badStreamUrl1   = "rtmp://sfo.contribute.live-video.net/app/fake1"
	badStreamUrl2   = "rtmp://localhost:1934/live/stream2"
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
	playlist         string
	codec            params.MimeType
	output           params.OutputType
}

func TestEgress(t *testing.T) {
	conf := getTestConfig(t)

	var room *lksdk.Room
	if conf.HasConnectionInfo {
		var err error
		room, err = lksdk.ConnectToRoom(conf.WsUrl, lksdk.ConnectInfo{
			APIKey:              conf.ApiKey,
			APISecret:           conf.ApiSecret,
			RoomName:            conf.RoomName,
			ParticipantName:     "sample",
			ParticipantIdentity: fmt.Sprintf("sample-%d", rand.Intn(100)),
		})
		require.NoError(t, err)
		defer room.Disconnect()
	}

	if conf.RunServiceTest {
		if !conf.HasRedis || !conf.HasConnectionInfo {
			t.Fatal("redis and connection info required for service test")
		}

		if !t.Run("Service", func(t *testing.T) {
			testService(t, conf, room)
		}) {
			t.FailNow()
		}
	}

	if conf.RunRoomTests {
		if !t.Run("RoomComposite", func(t *testing.T) {
			testRoomComposite(t, conf, room)
		}) {
			t.FailNow()
		}
	}

	if conf.RunTrackCompositeTests {
		if !conf.HasConnectionInfo {
			t.Fatal("connection info required for track composite tests")
		}

		if !t.Run("TrackComposite", func(t *testing.T) {
			testTrackComposite(t, conf, room)
		}) {
			t.FailNow()
		}
	}

	if conf.RunTrackTests {
		if !conf.HasConnectionInfo {
			t.Fatal("connection info required for track tests")
		}

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
	opts := []lksdk.ReaderSampleProviderOption{
		lksdk.ReaderTrackWithOnWriteComplete(func() {
			close(done)
			if pub != nil {
				_ = room.LocalParticipant.UnpublishTrack(pub.SID())
			}
		}),
	}

	if frameDuration != 0 {
		opts = append(opts, lksdk.ReaderTrackWithFrameDuration(frameDuration))
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
	ctx := context.Background()

	p, err := params.GetPipelineParams(ctx, conf.Config, req)
	require.NoError(t, err)

	if !strings.HasPrefix(conf.ApiKey, "API") || test.forceCustomInput {
		p.CustomInputURL = test.inputUrl
	}

	rec, err := pipeline.New(ctx, conf.Config, p)
	require.NoError(t, err)

	// record for ~30s. Takes about 5s to start
	time.AfterFunc(time.Second*35, func() {
		rec.SendEOS(ctx)
	})
	res := rec.Run(ctx)

	verifyFile(t, filepath, p, res, conf.Muting)
}

func runStreamTest(t *testing.T, conf *testConfig, req *livekit.StartEgressRequest, customUrl string) {
	ctx := context.Background()

	p, err := params.GetPipelineParams(ctx, conf.Config, req)
	require.NoError(t, err)
	if customUrl != "" {
		p.CustomInputURL = customUrl
	}

	rec, err := pipeline.New(ctx, conf.Config, p)
	require.NoError(t, err)

	t.Cleanup(func() {
		rec.SendEOS(ctx)
	})

	resChan := make(chan *livekit.EgressInfo, 1)
	go func() {
		resChan <- rec.Run(ctx)
	}()

	// wait for recorder to start
	time.Sleep(time.Second * 10)

	// check stream
	verifyStreams(t, p, streamUrl1)

	// add another, check both
	require.Error(t, rec.UpdateStream(ctx, &livekit.UpdateStreamRequest{
		EgressId:      req.EgressId,
		AddOutputUrls: []string{badStreamUrl1, streamUrl2, badStreamUrl2},
	}))
	time.Sleep(time.Second)
	verifyStreams(t, p, streamUrl1, streamUrl2)

	// remove first, check second
	require.NoError(t, rec.UpdateStream(ctx, &livekit.UpdateStreamRequest{
		EgressId:         req.EgressId,
		RemoveOutputUrls: []string{streamUrl1},
	}))
	verifyStreams(t, p, streamUrl2)

	// stop
	rec.SendEOS(ctx)
	res := <-resChan

	// egress info
	require.Empty(t, res.Error)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// stream info
	require.Len(t, res.GetStream().Info, 2)
	for _, info := range res.GetStream().Info {
		require.NotEmpty(t, info.Url)
		require.NotZero(t, info.Duration)
	}
}

func testStreamFailure(t *testing.T, conf *testConfig, customUrl string) {
	ctx := context.Background()

	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().Unix(),
		Request: &livekit.StartEgressRequest_RoomComposite{
			RoomComposite: &livekit.RoomCompositeEgressRequest{
				RoomName: conf.RoomName,
				Layout:   "speaker-dark",
				Output: &livekit.RoomCompositeEgressRequest_Stream{
					Stream: &livekit.StreamOutput{
						Protocol: livekit.StreamProtocol_RTMP,
						Urls:     []string{badStreamUrl1},
					},
				},
			},
		},
	}

	p, err := params.GetPipelineParams(ctx, conf.Config, req)
	require.NoError(t, err)
	if customUrl != "" {
		p.CustomInputURL = customUrl
	}

	rec, err := pipeline.New(ctx, conf.Config, p)
	require.NoError(t, err)

	info := rec.Run(ctx)
	require.NotEmpty(t, info.Error)
	require.Equal(t, livekit.EgressStatus_EGRESS_FAILED, info.Status)
}

func runSegmentsTest(t *testing.T, conf *testConfig, test *testCase, req *livekit.StartEgressRequest, playlistPath string) {
	ctx := context.Background()

	p, err := params.GetPipelineParams(ctx, conf.Config, req)
	require.NoError(t, err)

	if !strings.HasPrefix(conf.ApiKey, "API") || test.forceCustomInput {
		p.CustomInputURL = test.inputUrl
	}

	rec, err := pipeline.New(ctx, conf.Config, p)
	require.NoError(t, err)

	// record for ~30s. Takes about 5s to start
	time.AfterFunc(time.Second*35, func() {
		rec.SendEOS(ctx)
	})
	res := rec.Run(ctx)

	// egress info
	require.Empty(t, res.Error)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	verify(t, playlistPath, p, res, ResultTypeSegments, conf.Muting)
}
