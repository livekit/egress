//go:build integration
// +build integration

package test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go"

	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/egress/pkg/pipeline/params"
)

func testTrack(t *testing.T, conf *testConfig, room *lksdk.Room) {
	if conf.RunFileTests {
		for _, test := range []*testCase{
			{
				name:      "track-opus",
				audioOnly: true,
				codec:     params.MimeTypeOpus,
				filename:  fmt.Sprintf("track-opus-%v.ogg", time.Now().Unix()),
			},
			{
				name:      "track-vp8",
				videoOnly: true,
				codec:     params.MimeTypeVP8,
				filename:  fmt.Sprintf("track-vp8-%v.ivf", time.Now().Unix()),
			},
			{
				name:      "track-h264",
				videoOnly: true,
				codec:     params.MimeTypeH264,
				filename:  fmt.Sprintf("track-h264-%v.mp4", time.Now().Unix()),
			},
		} {
			if !t.Run(test.name, func(t *testing.T) {
				runTrackFileTest(t, conf, room, test)
			}) {
				t.FailNow()
			}
		}
	}

	if conf.RunStreamTests {
		for _, test := range []*testCase{
			{
				name:      "track-websocket",
				audioOnly: true,
				codec:     params.MimeTypeOpus,
				output:    params.OutputTypeRaw,
				filename:  fmt.Sprintf("track-ws-%v.raw", time.Now().Unix()),
			},
		} {
			if !t.Run(test.name, func(t *testing.T) {
				runTrackWebsocketTest(t, conf, room, test)
			}) {
				t.FailNow()
			}
		}
	}
}

func runTrackFileTest(t *testing.T, conf *testConfig, room *lksdk.Room, test *testCase) {
	trackID := publishSampleToRoom(t, room, test.codec, conf.Muting)
	t.Cleanup(func() {
		_ = room.LocalParticipant.UnpublishTrack(trackID)
		time.Sleep(time.Second)
	})

	filepath := getFilePath(conf.Config, test.filename)
	trackRequest := &livekit.TrackEgressRequest{
		RoomName: room.Name,
		TrackId:  trackID,
		Output: &livekit.TrackEgressRequest_File{
			File: &livekit.DirectFileOutput{
				Filepath: filepath,
			},
		},
	}

	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().UnixNano(),
		Request: &livekit.StartEgressRequest_Track{
			Track: trackRequest,
		},
	}

	runFileTest(t, conf, test, req, filepath)
}

func runTrackWebsocketTest(t *testing.T, conf *testConfig, room *lksdk.Room, test *testCase) {
	trackID := publishSampleToRoom(t, room, test.codec, false)
	t.Cleanup(func() {
		_ = room.LocalParticipant.UnpublishTrack(trackID)
		time.Sleep(time.Second)
	})

	time.Sleep(time.Second)

	filepath := getFilePath(conf.Config, test.filename)
	wss := newTestWebsocketServer(filepath, test.output)
	s := httptest.NewServer(http.HandlerFunc(wss.handleWebsocket))
	defer func() {
		wss.close()
		s.Close()
	}()

	trackRequest := &livekit.TrackEgressRequest{
		RoomName: room.Name,
		TrackId:  trackID,
		Output: &livekit.TrackEgressRequest_WebsocketUrl{
			WebsocketUrl: "ws" + strings.TrimPrefix(s.URL, "http"),
		},
	}

	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().UnixNano(),
		Request: &livekit.StartEgressRequest_Track{
			Track: trackRequest,
		},
	}

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

	verify(t, filepath, p, res, ResultTypeStream, conf.Muting)
}

type websocketTestServer struct {
	path   string
	file   *os.File
	conn   *websocket.Conn
	done   chan struct{}
	output params.OutputType
}

func newTestWebsocketServer(filepath string, output params.OutputType) *websocketTestServer {
	return &websocketTestServer{
		path:   filepath,
		done:   make(chan struct{}),
		output: output,
	}
}

func (s *websocketTestServer) handleWebsocket(w http.ResponseWriter, r *http.Request) {
	var err error

	s.file, err = os.Create(s.path)
	if err != nil {
		logger.Errorw("could not create file", err)
		return
	}

	// accept ws connection
	upgrader := websocket.Upgrader{}
	s.conn, err = upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Errorw("could not accept ws connection", err)
		return
	}

	go func() {
		defer func() {
			_ = s.file.Close()

			// close the connection only if it's not closed already
			if !websocket.IsUnexpectedCloseError(err) {
				_ = s.conn.Close()
			}
		}()

		for {
			select {
			case <-s.done:
				return
			default:
				mt, msg, err := s.conn.ReadMessage()
				if err != nil {
					if !websocket.IsUnexpectedCloseError(err) {
						logger.Errorw("unexpected ws close", err)
					}
					return
				}

				switch mt {
				case websocket.BinaryMessage:
					_, err = s.file.Write(msg)
					if err != nil {
						logger.Errorw("could not write to file", err)
						return
					}
				}
			}
		}
	}()
}

func (s *websocketTestServer) close() {
	close(s.done)
}
