//go:build integration

package test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go"
)

const (
	streamUrl1   = "rtmp://localhost:1935/live/stream1"
	streamUrl2   = "rtmp://localhost:1935/live/stream2"
	badStreamUrl = "rtmp://sfo.contribute.live-video.net/app/fake1"
	webUrl       = "https://www.youtube.com/watch?v=wjQq0nSGS28&t=5205s"
)

type testCase struct {
	name           string
	audioOnly      bool
	videoOnly      bool
	filename       string
	sessionTimeout time.Duration

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

func RunTestSuite(t *testing.T, conf *TestConfig, rpcClient egress.RPCClient, rpcServer egress.RPCServer) {
	// connect to room
	room, err := lksdk.ConnectToRoom(conf.WsUrl, lksdk.ConnectInfo{
		APIKey:              conf.ApiKey,
		APISecret:           conf.ApiSecret,
		RoomName:            conf.RoomName,
		ParticipantName:     "egress-sample",
		ParticipantIdentity: fmt.Sprintf("sample-%d", rand.Intn(100)),
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)
	defer room.Disconnect()

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
	if conf.runRoomTests {
		if conf.runFileTests {
			t.Run("RoomComposite/File", func(t *testing.T) {
				testRoomCompositeFile(t, conf)
			})
		}

		if conf.runStreamTests {
			t.Run("RoomComposite/Stream", func(t *testing.T) {
				testRoomCompositeStream(t, conf)
			})
		}

		if conf.runSegmentTests {
			t.Run("RoomComposite/Segments", func(t *testing.T) {
				testRoomCompositeSegments(t, conf)
			})
		}
	}

	if conf.runTrackCompositeTests {
		if conf.runFileTests {
			t.Run("TrackComposite/File", func(t *testing.T) {
				testTrackCompositeFile(t, conf)
			})
		}

		if conf.runStreamTests {
			t.Run("TrackComposite/Stream", func(t *testing.T) {
				testTrackCompositeStream(t, conf)
			})
		}

		if conf.runSegmentTests {
			t.Run("TrackComposite/Segments", func(t *testing.T) {
				testTrackCompositeSegments(t, conf)
			})
		}
	}

	if conf.runTrackTests {
		if conf.runFileTests {
			t.Run("Track/File", func(t *testing.T) {
				testTrackFile(t, conf)
			})
		}

		if conf.runStreamTests {
			t.Run("Track/Stream", func(t *testing.T) {
				testTrackStream(t, conf)
			})
		}
	}

	if conf.runWebTests {
		if conf.runFileTests {
			t.Run("Web/File", func(t *testing.T) {
				testWebFile(t, conf)
			})
		}

		if conf.runStreamTests {
			t.Run("Web/Stream", func(t *testing.T) {
				testWebStream(t, conf)
			})
		}

		if conf.runSegmentTests {
			t.Run("Web/Segments", func(t *testing.T) {
				testWebSegments(t, conf)
			})
		}
	}
}

func awaitIdle(t *testing.T, svc *service.Service) {
	for i := 0; i < 30; i++ {
		status := getStatus(t, svc)
		if len(status) == 1 {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("service not idle after 30s")
}

func runFileTest(t *testing.T, conf *TestConfig, req *livekit.StartEgressRequest, test *testCase) {
	conf.SessionLimits.FileOutputMaxDuration = test.sessionTimeout

	// start
	egressID := startEgress(t, conf, req)

	var res *livekit.EgressInfo
	if conf.SessionLimits.FileOutputMaxDuration > 0 {
		time.Sleep(conf.SessionLimits.FileOutputMaxDuration + time.Second)

		res = checkStoppedEgress(t, conf, egressID, livekit.EgressStatus_EGRESS_LIMIT_REACHED)
	} else {
		time.Sleep(time.Second * 25)

		// stop
		res = stopEgress(t, conf, egressID)
	}

	// get params
	p, err := params.GetPipelineParams(context.Background(), conf.Config, req)
	require.NoError(t, err)
	if p.OutputType == "" {
		p.OutputType = test.outputType
	}

	// verify
	verifyFile(t, conf, p, res)
}

func runStreamTest(t *testing.T, conf *TestConfig, req *livekit.StartEgressRequest, sessionTimeout time.Duration) {
	conf.SessionLimits.StreamOutputMaxDuration = sessionTimeout

	if conf.SessionLimits.StreamOutputMaxDuration > 0 {
		runTimeLimitStreamTest(t, conf, req)
	} else {
		runMultipleStreamTest(t, conf, req)
	}
}

func runTimeLimitStreamTest(t *testing.T, conf *TestConfig, req *livekit.StartEgressRequest) {
	ctx := context.Background()
	egressID := startEgress(t, conf, req)

	time.Sleep(time.Second * 5)

	// get params
	p, err := params.GetPipelineParams(ctx, conf.Config, req)
	require.NoError(t, err)

	verifyStreams(t, p, streamUrl1)

	time.Sleep(conf.SessionLimits.StreamOutputMaxDuration - time.Second*4)

	checkStoppedEgress(t, conf, egressID, livekit.EgressStatus_EGRESS_LIMIT_REACHED)
}

func runMultipleStreamTest(t *testing.T, conf *TestConfig, req *livekit.StartEgressRequest) {
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
				AddOutputUrls: []string{badStreamUrl, streamUrl2},
			},
		},
	})
	require.NoError(t, err)

	time.Sleep(time.Second * 5)

	update := getUpdate(t, conf.updates, egressID)
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE.String(), update.Status.String())
	require.Len(t, update.GetStream().Info, 3)
	for _, info := range update.GetStream().Info {
		switch info.Url {
		case streamUrl1, streamUrl2:
			require.Equal(t, livekit.StreamInfo_ACTIVE.String(), info.Status.String())

		case badStreamUrl:
			require.Equal(t, livekit.StreamInfo_FAILED.String(), info.Status.String())

		default:
			t.Fatal("invalid stream url in result")
		}
	}

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

	time.Sleep(time.Second * 10)

	// stop
	res := stopEgress(t, conf, egressID)

	// verify egress info
	require.Empty(t, res.Error)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// check stream info
	require.Len(t, res.GetStream().Info, 3)
	for _, info := range res.GetStream().Info {
		require.NotZero(t, info.StartedAt)
		require.NotZero(t, info.EndedAt)

		switch info.Url {
		case streamUrl1:
			require.Equal(t, livekit.StreamInfo_FINISHED.String(), info.Status.String())
			require.Greater(t, float64(info.Duration)/1e9, 15.0)

		case streamUrl2:
			require.Equal(t, livekit.StreamInfo_FINISHED.String(), info.Status.String())
			require.Greater(t, float64(info.Duration)/1e9, 10.0)

		case badStreamUrl:
			require.Equal(t, livekit.StreamInfo_FAILED.String(), info.Status.String())

		default:
			t.Fatal("invalid stream url in result")
		}
	}
}

func runSegmentsTest(t *testing.T, conf *TestConfig, req *livekit.StartEgressRequest, sessionTimeout time.Duration) {
	conf.SessionLimits.SegmentOutputMaxDuration = sessionTimeout

	egressID := startEgress(t, conf, req)

	var res *livekit.EgressInfo
	if conf.SessionLimits.SegmentOutputMaxDuration > 0 {
		time.Sleep(conf.SessionLimits.SegmentOutputMaxDuration + time.Second)

		res = checkStoppedEgress(t, conf, egressID, livekit.EgressStatus_EGRESS_LIMIT_REACHED)
	} else {
		time.Sleep(time.Second * 25)

		// stop
		res = stopEgress(t, conf, egressID)
	}

	// get params
	p, err := params.GetPipelineParams(context.Background(), conf.Config, req)
	require.NoError(t, err)

	verifySegments(t, conf, p, res)
}

func startEgress(t *testing.T, conf *TestConfig, req *livekit.StartEgressRequest) string {
	// send start request
	info, err := conf.rpcClient.SendRequest(context.Background(), req)

	// check returned egress info
	require.NoError(t, err)
	require.Empty(t, info.Error)
	require.NotEmpty(t, info.EgressId)
	switch req.Request.(type) {
	case *livekit.StartEgressRequest_Web:
		require.Empty(t, info.RoomName)
	default:
		require.Equal(t, conf.RoomName, info.RoomName)
	}

	require.Equal(t, livekit.EgressStatus_EGRESS_STARTING.String(), info.Status.String())

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

func getStatus(t *testing.T, svc *service.Service) map[string]interface{} {
	b, err := svc.Status()
	require.NoError(t, err)

	status := make(map[string]interface{})
	err = json.Unmarshal(b, &status)
	require.NoError(t, err)

	return status
}

func checkUpdate(t *testing.T, sub utils.PubSub, egressID string, status livekit.EgressStatus) *livekit.EgressInfo {
	info := getUpdate(t, sub, egressID)

	require.Equal(t, status.String(), info.Status.String())
	if info.Status == livekit.EgressStatus_EGRESS_FAILED {
		require.NotEmpty(t, info.Error, "failed egress missing error")
	} else {
		require.Empty(t, info.Error, "status %s with error %s", info.Status.String(), info.Error)
	}

	return info
}

func getUpdate(t *testing.T, sub utils.PubSub, egressID string) *livekit.EgressInfo {
	for {
		select {
		case msg := <-sub.Channel():
			b := sub.Payload(msg)
			info := &livekit.EgressInfo{}
			require.NoError(t, proto.Unmarshal(b, info))
			if info.EgressId == egressID {
				return info
			}

		case <-time.After(time.Second * 45):
			t.Fatal("no update from results channel")
			return nil
		}
	}
}

func stopEgress(t *testing.T, conf *TestConfig, egressID string) *livekit.EgressInfo {
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
	require.Equal(t, livekit.EgressStatus_EGRESS_ENDING.String(), info.Status.String())

	// check complete update
	return checkStoppedEgress(t, conf, egressID, livekit.EgressStatus_EGRESS_COMPLETE)
}

func checkStoppedEgress(t *testing.T, conf *TestConfig, egressID string, expectedStatus livekit.EgressStatus) *livekit.EgressInfo {
	// check ending update
	checkUpdate(t, conf.updates, egressID, livekit.EgressStatus_EGRESS_ENDING)

	// get final info
	info := checkUpdate(t, conf.updates, egressID, expectedStatus)

	// check status
	if conf.HealthPort != 0 {
		status := getStatus(t, conf.svc)
		require.Len(t, status, 1)
	}

	return info
}
