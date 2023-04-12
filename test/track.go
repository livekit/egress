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

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

func runTrackTests(t *testing.T, conf *TestConfig) {
	if !conf.runTrackTests {
		return
	}

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

func testTrackFile(t *testing.T, conf *TestConfig) {
	for _, test := range []*testCase{
		{
			name:       "track-opus",
			audioOnly:  true,
			audioCodec: types.MimeTypeOpus,
			outputType: types.OutputTypeOGG,
			filename:   "t_{track_source}_{time}.ogg",
		},
		{
			name:       "track-vp8",
			videoOnly:  true,
			videoCodec: types.MimeTypeVP8,
			outputType: types.OutputTypeWebM,
			filename:   "t_{track_type}_{time}.webm",
		},
		{
			name:       "track-h264",
			videoOnly:  true,
			videoCodec: types.MimeTypeH264,
			outputType: types.OutputTypeMP4,
			filename:   "t_{track_id}_{time}.mp4",
		},
		{
			name:           "track-limit",
			videoOnly:      true,
			videoCodec:     types.MimeTypeH264,
			outputType:     types.OutputTypeMP4,
			filename:       "t_{room_name}_limit_{time}.mp4",
			sessionTimeout: time.Second * 20,
		},
	} {
		runSDKTest(t, conf, test.name, test.audioCodec, test.videoCodec, func(t *testing.T, audioTrackID, videoTrackID string) {
			trackID := audioTrackID
			if trackID == "" {
				trackID = videoTrackID
			}

			trackRequest := &livekit.TrackEgressRequest{
				RoomName: conf.room.Name(),
				TrackId:  trackID,
				Output: &livekit.TrackEgressRequest_File{
					File: &livekit.DirectFileOutput{
						Filepath: getFilePath(conf.ServiceConfig, test.filename),
					},
				},
			}

			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_Track{
					Track: trackRequest,
				},
			}

			runFileTest(t, conf, req, test)
		})
		if conf.Short {
			return
		}
	}
}

func testTrackStream(t *testing.T, conf *TestConfig) {
	now := time.Now().Unix()
	for _, test := range []*testCase{
		{
			name:       "track-websocket",
			audioOnly:  true,
			audioCodec: types.MimeTypeOpus,
			filename:   fmt.Sprintf("track-ws-%v.raw", now),
		},
		{
			name:       "track-websocket-limit",
			audioOnly:  true,
			audioCodec: types.MimeTypeOpus,
			filename:   fmt.Sprintf("track-ws-limit-%v.raw", now),
		},
	} {
		runSDKTest(t, conf, test.name, test.audioCodec, test.videoCodec, func(t *testing.T, audioTrackID, videoTrackID string) {
			trackID := audioTrackID
			if trackID == "" {
				trackID = videoTrackID
			}

			conf.SessionLimits.StreamOutputMaxDuration = test.sessionTimeout

			filepath := getFilePath(conf.ServiceConfig, test.filename)
			wss := newTestWebsocketServer(filepath)
			s := httptest.NewServer(http.HandlerFunc(wss.handleWebsocket))
			defer func() {
				wss.close()
				s.Close()
			}()

			req := &rpc.StartEgressRequest{
				EgressId: utils.NewGuid(utils.EgressPrefix),
				Request: &rpc.StartEgressRequest_Track{
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

			p, err := config.GetValidatedPipelineConfig(conf.ServiceConfig, req)
			require.NoError(t, err)
			p.GstReady = make(chan struct{})

			rec, err := pipeline.New(ctx, p, nil)
			require.NoError(t, err)

			if conf.SessionLimits.StreamOutputMaxDuration >= 0 {
				// record for ~30s. Takes about 5s to start
				time.AfterFunc(time.Second*35, func() {
					rec.SendEOS(ctx)
				})
			}
			res := rec.Run(ctx)

			verify(t, filepath, p, res, types.EgressTypeWebsocket, conf.Muting, conf.sourceFramerate)
		})
		if conf.Short {
			return
		}
	}
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
