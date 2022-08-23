//go:build integration

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

	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
)

func testTrack(t *testing.T, conf *testConfig) {
	now := time.Now().Unix()
	if !conf.StreamTestsOnly && !conf.SegmentedFileTestsOnly {
		for _, test := range []*testCase{
			{
				name:       "track-opus",
				audioOnly:  true,
				audioCodec: params.MimeTypeOpus,
				outputType: params.OutputTypeOGG,
				filename:   fmt.Sprintf("track-opus-%v.ogg", now),
			},
			{
				name:       "track-vp8",
				videoOnly:  true,
				videoCodec: params.MimeTypeVP8,
				outputType: params.OutputTypeIVF,
				filename:   fmt.Sprintf("track-vp8-%v.ivf", now),
			},
			{
				name:       "track-h264",
				videoOnly:  true,
				videoCodec: params.MimeTypeH264,
				outputType: params.OutputTypeMP4,
				filename:   fmt.Sprintf("track-h264-%v.mp4", now),
			},
			{
				name:           "track-h264-timedout",
				videoOnly:      true,
				videoCodec:     params.MimeTypeH264,
				outputType:     params.OutputTypeMP4,
				filename:       fmt.Sprintf("track-h264-timedout-%v.mp4", now),
				sessionTimeout: 20 * time.Second,
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				runTrackFileTest(t, conf, test)
			})
		}
	}

	if !conf.FileTestsOnly && !conf.SegmentedFileTestsOnly {
		for _, test := range []*testCase{
			{
				name:       "track-websocket",
				audioOnly:  true,
				audioCodec: params.MimeTypeOpus,
				filename:   fmt.Sprintf("track-ws-%v.raw", now),
			},
			{
				name:       "track-websocket-timedout",
				audioOnly:  true,
				audioCodec: params.MimeTypeOpus,
				filename:   fmt.Sprintf("track-ws-timedout-%v.raw", now),
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				runTrackWebsocketTest(t, conf, test)
			})
		}
	}
}

func runTrackFileTest(t *testing.T, conf *testConfig, test *testCase) {
	codec := test.videoCodec
	if test.audioOnly {
		codec = test.audioCodec
	}
	trackID := publishSampleToRoom(t, conf.room, codec, conf.Muting)
	time.Sleep(time.Second)

	filepath := getFilePath(conf.Config, test.filename)
	trackRequest := &livekit.TrackEgressRequest{
		RoomName: conf.room.Name(),
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

	runFileTest(t, conf, req, test, filepath)
}

func runTrackWebsocketTest(t *testing.T, conf *testConfig, test *testCase) {
	conf.SessionLimits.StreamOutputMaxDuration = test.sessionTimeout

	codec := test.videoCodec
	if test.audioCodec != "" {
		codec = test.audioCodec
	}
	trackID := publishSampleToRoom(t, conf.room, codec, false)
	time.Sleep(time.Second)

	filepath := getFilePath(conf.Config, test.filename)
	wss := newTestWebsocketServer(filepath)
	s := httptest.NewServer(http.HandlerFunc(wss.handleWebsocket))
	defer func() {
		wss.close()
		s.Close()
	}()

	req := &livekit.StartEgressRequest{
		EgressId: utils.NewGuid(utils.EgressPrefix),
		Request: &livekit.StartEgressRequest_Track{
			Track: &livekit.TrackEgressRequest{
				RoomName: conf.room.Name(),
				TrackId:  trackID,
				Output: &livekit.TrackEgressRequest_WebsocketUrl{
					WebsocketUrl: "ws" + strings.TrimPrefix(s.URL, "http"),
				},
			},
		},
	}

	ctx := context.Background()

	p, err := params.GetPipelineParams(ctx, conf.Config, req)
	require.NoError(t, err)

	rec, err := pipeline.New(ctx, conf.Config, p)
	require.NoError(t, err)

	if conf.SessionLimits.StreamOutputMaxDuration >= 0 {
		// record for ~30s. Takes about 5s to start
		time.AfterFunc(time.Second*35, func() {
			rec.SendEOS(ctx)
		})
	}
	res := rec.Run(ctx)

	verify(t, filepath, p, res, ResultTypeStream, conf.Muting)
}

type websocketTestServer struct {
	path string
	file *os.File
	conn *websocket.Conn
	done chan struct{}
}

func newTestWebsocketServer(filepath string) *websocketTestServer {
	return &websocketTestServer{
		path: filepath,
		done: make(chan struct{}),
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
