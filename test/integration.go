//go:build integration

package test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/livekit-server/pkg/service/rpc"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"
	lksdk "github.com/livekit/server-sdk-go"
)

const (
	streamUrl1   = "rtmp://localhost:1935/live/stream1"
	streamUrl2   = "rtmp://localhost:1935/live/stream2"
	badStreamUrl = "rtmp://sfo.contribute.live-video.net/app/fake1"
	webUrl       = "https://www.youtube.com/watch?v=wjQq0nSGS28&t=5205s"
	// v2           = true
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
	audioCodec types.MimeType
	videoCodec types.MimeType

	// used by track tests
	outputType types.OutputType

	expectVideoTranscoding bool
}

func RunTestSuite(t *testing.T, conf *TestConfig, rpcClient egress.RPCClient, rpcServer egress.RPCServer, bus psrpc.MessageBus, templateFs fs.FS) {
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
	ioClient, err := rpc.NewIOInfoClient("test_io_client", bus)
	require.NoError(t, err)
	svc, err := service.NewService(conf.ServiceConfig, bus, rpcServer, ioClient)
	require.NoError(t, err)

	psrpcClient, err := rpc.NewEgressClient(livekit.NodeID(utils.NewGuid("TEST_")), bus)
	require.NoError(t, err)

	// start debug handler
	svc.StartDebugHandlers()

	// start templates handler
	err = svc.StartTemplatesServer(templateFs)
	require.NoError(t, err)

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

	psrpcUpdates := make(chan *livekit.EgressInfo, 100)
	_, err = newIOTestServer(bus, psrpcUpdates)
	require.NoError(t, err)

	// update test config
	conf.svc = svc
	conf.rpcClient = rpcClient
	conf.psrpcClient = psrpcClient
	conf.updates = updates
	conf.psrpcUpdates = psrpcUpdates
	conf.room = room

	// check status
	if conf.HealthPort != 0 {
		status := getStatus(t, svc)
		require.Len(t, status, 1)
		require.Contains(t, status, "CpuLoad")
	}

	// run tests
	if conf.runRoomTests {
		conf.sourceFramerate = 30

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
		conf.sourceFramerate = 23.97

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
		conf.sourceFramerate = 23.97

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
		conf.sourceFramerate = 30

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
	svc.KillAll()
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
	p, err := config.GetValidatedPipelineConfig(conf.ServiceConfig, req)
	require.NoError(t, err)
	if p.Outputs[types.EgressTypeFile].OutputType == types.OutputTypeUnknown {
		p.Outputs[types.EgressTypeFile].OutputType = test.outputType
	}

	require.Equal(t, test.expectVideoTranscoding, p.VideoTranscoding)

	// verify
	verifyFile(t, conf, p, res)
}

func runStreamTest(t *testing.T, conf *TestConfig, req *livekit.StartEgressRequest, test *testCase) {
	conf.SessionLimits.StreamOutputMaxDuration = test.sessionTimeout

	if conf.SessionLimits.StreamOutputMaxDuration > 0 {
		runTimeLimitStreamTest(t, conf, req, test)
	} else {
		runMultipleStreamTest(t, conf, req, test)
	}
}

func runTimeLimitStreamTest(t *testing.T, conf *TestConfig, req *livekit.StartEgressRequest, test *testCase) {
	egressID := startEgress(t, conf, req)

	time.Sleep(time.Second * 5)

	// get params
	p, err := config.GetValidatedPipelineConfig(conf.ServiceConfig, req)
	require.NoError(t, err)

	require.Equal(t, test.expectVideoTranscoding, p.VideoTranscoding)

	verifyStreams(t, p, conf, streamUrl1)

	time.Sleep(conf.SessionLimits.StreamOutputMaxDuration - time.Second*4)

	checkStoppedEgress(t, conf, egressID, livekit.EgressStatus_EGRESS_LIMIT_REACHED)
}

func runMultipleStreamTest(t *testing.T, conf *TestConfig, req *livekit.StartEgressRequest, test *testCase) {
	ctx := context.Background()
	egressID := startEgress(t, conf, req)

	time.Sleep(time.Second * 5)

	// get params
	p, err := config.GetValidatedPipelineConfig(conf.ServiceConfig, req)
	require.NoError(t, err)

	// verify stream
	require.Equal(t, test.expectVideoTranscoding, p.VideoTranscoding)
	verifyStreams(t, p, conf, streamUrl1)

	// add one good stream url and a couple bad ones
	if conf.PSRPC {
		_, err = conf.psrpcClient.UpdateStream(ctx, egressID, &livekit.UpdateStreamRequest{
			EgressId:      egressID,
			AddOutputUrls: []string{badStreamUrl, streamUrl2},
		})
	} else {
		_, err = conf.rpcClient.SendRequest(ctx, &livekit.EgressRequest{
			EgressId: egressID,
			Request: &livekit.EgressRequest_UpdateStream{
				UpdateStream: &livekit.UpdateStreamRequest{
					EgressId:      req.EgressId,
					AddOutputUrls: []string{badStreamUrl, streamUrl2},
				},
			},
		})
	}

	require.NoError(t, err)

	time.Sleep(time.Second * 5)

	update := getUpdate(t, conf, egressID)
	require.Equal(t, livekit.EgressStatus_EGRESS_ACTIVE.String(), update.Status.String())
	var streams []*livekit.StreamInfo
	// if v2 {
	// 	streams = update.StreamResults
	// } else {
	streams = update.GetStream().Info
	// }
	require.Len(t, streams, 3)
	for _, info := range streams {
		switch info.Url {
		case streamUrl1, streamUrl2:
			require.Equal(t, livekit.StreamInfo_ACTIVE.String(), info.Status.String())

		case badStreamUrl:
			require.Equal(t, livekit.StreamInfo_FAILED.String(), info.Status.String())

		default:
			t.Fatal("invalid stream url in result")
		}
	}

	require.Equal(t, test.expectVideoTranscoding, p.VideoTranscoding)

	// verify the good stream urls
	verifyStreams(t, p, conf, streamUrl1, streamUrl2)

	// remove one of the stream urls
	if conf.PSRPC {
		_, err = conf.psrpcClient.UpdateStream(ctx, egressID, &livekit.UpdateStreamRequest{
			EgressId:         egressID,
			RemoveOutputUrls: []string{streamUrl1},
		})
	} else {
		_, err = conf.rpcClient.SendRequest(ctx, &livekit.EgressRequest{
			EgressId: egressID,
			Request: &livekit.EgressRequest_UpdateStream{
				UpdateStream: &livekit.UpdateStreamRequest{
					EgressId:         req.EgressId,
					RemoveOutputUrls: []string{streamUrl1},
				},
			},
		})
	}
	require.NoError(t, err)

	time.Sleep(time.Second * 5)

	// verify the remaining stream
	verifyStreams(t, p, conf, streamUrl2)

	time.Sleep(time.Second * 10)

	// stop
	res := stopEgress(t, conf, egressID)

	// verify egress info
	require.Empty(t, res.Error)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// check stream info
	// if v2 {
	// 	streams = res.StreamResults
	// } else {
	streams = res.GetStream().Info
	// }
	require.Len(t, streams, 3)
	for _, info := range streams {
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

func runSegmentsTest(t *testing.T, conf *TestConfig, req *livekit.StartEgressRequest, test *testCase) {
	conf.SessionLimits.SegmentOutputMaxDuration = test.sessionTimeout

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
	p, err := config.GetValidatedPipelineConfig(conf.ServiceConfig, req)
	require.NoError(t, err)

	require.Equal(t, test.expectVideoTranscoding, p.VideoTranscoding)
	verifySegments(t, conf, p, res)
}

func startEgress(t *testing.T, conf *TestConfig, req *livekit.StartEgressRequest) string {
	// send start request
	var info *livekit.EgressInfo
	var err error
	if conf.PSRPC {
		info, err = conf.psrpcClient.StartEgress(context.Background(), "", req)
	} else {
		info, err = conf.rpcClient.SendRequest(context.Background(), req)
	}

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
	checkUpdate(t, conf, info.EgressId, livekit.EgressStatus_EGRESS_ACTIVE)

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

func stopEgress(t *testing.T, conf *TestConfig, egressID string) *livekit.EgressInfo {
	// send stop request
	var info *livekit.EgressInfo
	var err error
	if conf.PSRPC {
		info, err = conf.psrpcClient.StopEgress(context.Background(), egressID, &livekit.StopEgressRequest{
			EgressId: egressID,
		})
	} else {
		info, err = conf.rpcClient.SendRequest(context.Background(), &livekit.EgressRequest{
			EgressId: egressID,
			Request: &livekit.EgressRequest_Stop{
				Stop: &livekit.StopEgressRequest{
					EgressId: egressID,
				},
			},
		})
	}

	// check returned egress info
	require.NoError(t, err)
	require.Empty(t, info.Error)
	require.NotEmpty(t, info.StartedAt)
	require.Equal(t, livekit.EgressStatus_EGRESS_ENDING.String(), info.Status.String())

	// check complete update
	return checkStoppedEgress(t, conf, egressID, livekit.EgressStatus_EGRESS_COMPLETE)
}
