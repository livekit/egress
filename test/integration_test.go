//go:build integration

package test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/redis"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go"

	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/egress/pkg/pipeline/params"
)

const (
	streamUrl1    = "rtmp://localhost:1935/live/stream1"
	streamUrl2    = "rtmp://localhost:1935/live/stream2"
	badStreamUrl1 = "rtmp://sfo.contribute.live-video.net/app/fake1"
	badStreamUrl2 = "rtmp://localhost:1934/live/stream2"
)

type testCase struct {
	name      string
	audioOnly bool
	videoOnly bool
	filename  string

	// used by room and track composite tests
	fileType livekit.EncodedFileType
	options  *livekit.EncodingOptions

	// used by segmented file tests
	playlist string

	// used by track and track composite tests
	audioCodec params.MimeType
	videoCodec params.MimeType

	// used by track tests
	outputType params.OutputType
}

func TestEgress(t *testing.T) {
	conf := getTestConfig(t)

	// connect to room
	lksdk.SetLogger(logr.Discard())
	room, err := lksdk.ConnectToRoom(conf.WsUrl, lksdk.ConnectInfo{
		APIKey:              conf.ApiKey,
		APISecret:           conf.ApiSecret,
		RoomName:            conf.RoomName,
		ParticipantName:     "egress-sample",
		ParticipantIdentity: fmt.Sprintf("sample-%d", rand.Intn(100)),
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	defer room.Disconnect()

	// rpc client and server
	rc, err := redis.GetRedisClient(conf.Config.Redis)
	require.NoError(t, err)
	rpcServer := egress.NewRedisRPCServer(rc)
	rpcClient := egress.NewRedisRPCClient("egress_test", rc)

	// start service
	svc := service.NewService(conf.Config, rpcServer)
	go func() {
		err := svc.Run()
		require.NoError(t, err)
	}()
	t.Cleanup(func() { svc.Stop(true) })
	time.Sleep(time.Second * 3)

	// subscribe to update channel
	updates, err := rpcClient.GetUpdateChannel(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = updates.Close() })

	// update test config
	conf.svc = svc
	conf.rpcClient = rpcClient
	conf.updates = updates
	conf.room = room

	// check status
	if conf.HealthPort != 0 {
		status := getStatus(t, svc)
		require.Len(t, status, 1)
		require.Contains(t, status, "CpuLoad")
	}

	// run tests
	if !conf.TrackCompositeTestsOnly && !conf.TrackTestsOnly {
		t.Run("RoomComposite", func(t *testing.T) {
			testRoomComposite(t, conf)
		})
	}

	if !conf.RoomTestsOnly && !conf.TrackTestsOnly {
		t.Run("TrackComposite", func(t *testing.T) {
			testTrackComposite(t, conf)
		})
	}

	if !conf.RoomTestsOnly && !conf.TrackCompositeTestsOnly {
		t.Run("Track", func(t *testing.T) {
			testTrack(t, conf)
		})
	}
}

func runFileTest(t *testing.T, conf *testConfig, req *livekit.StartEgressRequest, test *testCase, filepath string) {
	// start
	egressID := startEgress(t, conf, req)

	time.Sleep(time.Second * 25)

	// stop
	res := stopEgress(t, conf, egressID)

	// get params
	p, err := params.GetPipelineParams(context.Background(), conf.Config, req)
	require.NoError(t, err)
	if p.OutputType == "" {
		p.OutputType = test.outputType
	}

	// verify
	verifyFile(t, filepath, p, res, conf.Muting)
}

func runStreamTest(t *testing.T, conf *testConfig, req *livekit.StartEgressRequest) {
	ctx := context.Background()
	egressID := startEgress(t, conf, req)

	time.Sleep(time.Second * 5)

	// get params
	p, err := params.GetPipelineParams(ctx, conf.Config, req)
	require.NoError(t, err)

	// verify stream
	verifyStreams(t, p, streamUrl1)

	// add one good stream url and a couple bad ones
	_, err = conf.rpcClient.SendRequest(ctx, &livekit.EgressRequest{
		EgressId: egressID,
		Request: &livekit.EgressRequest_UpdateStream{
			UpdateStream: &livekit.UpdateStreamRequest{
				EgressId:      req.EgressId,
				AddOutputUrls: []string{badStreamUrl1, streamUrl2, badStreamUrl2},
			},
		},
	})

	// should return an error
	require.Error(t, err)

	time.Sleep(time.Second * 5)

	// verify the good stream urls
	verifyStreams(t, p, streamUrl1, streamUrl2)

	// remove one of the stream urls
	_, err = conf.rpcClient.SendRequest(ctx, &livekit.EgressRequest{
		EgressId: egressID,
		Request: &livekit.EgressRequest_UpdateStream{
			UpdateStream: &livekit.UpdateStreamRequest{
				EgressId:         req.EgressId,
				RemoveOutputUrls: []string{streamUrl1},
			},
		},
	})
	require.NoError(t, err)

	time.Sleep(time.Second * 5)

	// verify the remaining stream
	verifyStreams(t, p, streamUrl2)

	// stop
	res := stopEgress(t, conf, egressID)

	// verify egress info
	require.Empty(t, res.Error)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// check that durations are reasonable
	require.Len(t, res.GetStream().Info, 2)
	for _, info := range res.GetStream().Info {
		switch info.Url {
		case streamUrl1:
			require.Greater(t, float64(info.Duration)/1e9, 15.0)
		case streamUrl2:
			require.Greater(t, float64(info.Duration)/1e9, 10.0)
		default:
			t.Fatal("invalid stream url in result")
		}
	}
}

func runSegmentsTest(t *testing.T, conf *testConfig, req *livekit.StartEgressRequest, playlistPath string) {
	ctx := context.Background()

	p, err := params.GetPipelineParams(ctx, conf.Config, req)
	require.NoError(t, err)

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

	base := playlistPath[:len(playlistPath)-5]
	playlistPath = download(t, p.FileUpload, playlistPath, playlistPath)
	segments := int(res.GetSegments().SegmentCount)
	for i := 0; i < segments; i++ {
		filename := fmt.Sprintf("%s_%05d.ts", base, i)
		download(t, p.FileUpload, filename, filename)
	}

	verify(t, playlistPath, p, res, ResultTypeSegments, conf.Muting)
}

func startEgress(t *testing.T, conf *testConfig, req *livekit.StartEgressRequest) string {
	// send start request
	info, err := conf.rpcClient.SendRequest(context.Background(), req)

	// check returned egress info
	require.NoError(t, err)
	require.Empty(t, info.Error)
	require.NotEmpty(t, info.EgressId)
	require.Equal(t, conf.RoomName, info.RoomName)
	require.Equal(t, livekit.EgressStatus_EGRESS_STARTING, info.Status)

	// check status
	if conf.HealthPort != 0 {
		status := getStatus(t, conf.svc)
		require.Contains(t, status, info.EgressId)
	}

	// wait
	time.Sleep(time.Second * 5)

	// check active update
	checkUpdate(t, conf.updates, info.EgressId, livekit.EgressStatus_EGRESS_ACTIVE)

	return info.EgressId
}

func stopEgress(t *testing.T, conf *testConfig, egressID string) *livekit.EgressInfo {
	// send stop request
	info, err := conf.rpcClient.SendRequest(context.Background(), &livekit.EgressRequest{
		EgressId: egressID,
		Request: &livekit.EgressRequest_Stop{
			Stop: &livekit.StopEgressRequest{
				EgressId: egressID,
			},
		},
	})

	// check returned egress info
	require.NoError(t, err)
	require.Empty(t, info.Error)
	require.NotEmpty(t, info.StartedAt)
	require.Equal(t, livekit.EgressStatus_EGRESS_ENDING, info.Status)

	// check ending update
	checkUpdate(t, conf.updates, egressID, livekit.EgressStatus_EGRESS_ENDING)

	// check complete update
	info = checkUpdate(t, conf.updates, egressID, livekit.EgressStatus_EGRESS_COMPLETE)

	// check status
	if conf.HealthPort != 0 {
		status := getStatus(t, conf.svc)
		require.Len(t, status, 1)
	}

	return info
}

func getStatus(t *testing.T, svc *service.Service) map[string]interface{} {
	b, err := svc.Status()
	require.NoError(t, err)

	status := make(map[string]interface{})
	err = json.Unmarshal(b, &status)
	require.NoError(t, err)

	return status
}

func checkUpdate(t *testing.T, sub utils.PubSub, egressID string, status livekit.EgressStatus) *livekit.EgressInfo {
	for {
		select {
		case msg := <-sub.Channel():
			b := sub.Payload(msg)
			info := &livekit.EgressInfo{}
			require.NoError(t, proto.Unmarshal(b, info))

			if info.EgressId != egressID {
				continue
			}

			require.Empty(t, info.Error)
			require.Equal(t, egressID, info.EgressId)
			require.Equal(t, status, info.Status)
			return info

		case <-time.After(time.Second * 30):
			t.Fatal("no update from results channel")
			return nil
		}
	}
}
