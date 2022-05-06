//go:build integration
// +build integration

package test

import (
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go"

	"github.com/livekit/livekit-egress/pkg/config"
)

func testTrackComposite(t *testing.T, conf *config.Config, room *lksdk.Room) {
	testTrackCompositeFile(t, conf, room, "opus", "vp8", []*testCase{
		{
			name:       "tc-vp8-mp4",
			fileType:   livekit.EncodedFileType_MP4,
			filePrefix: "tc-vp8",
		},
		{
			name:       "tc-opus-only-ogg",
			audioOnly:  true,
			fileType:   livekit.EncodedFileType_OGG,
			filePrefix: "tc-opus-only",
		},
	})

	testTrackCompositeFile(t, conf, room, "opus", "h264", []*testCase{
		{
			name:       "tc-h264-mp4",
			fileType:   livekit.EncodedFileType_MP4,
			filePrefix: "tc-h264",
		},
		{
			name:       "tc-h264-only-mp4",
			videoOnly:  true,
			fileType:   livekit.EncodedFileType_MP4,
			filePrefix: "tc-h264-only",
		},
	})
}

func testTrackCompositeFile(t *testing.T, conf *config.Config, room *lksdk.Room, audioCodec, videoCodec string, cases []*testCase) {
	p := publishSamplesToRoom(t, room, audioCodec, videoCodec)

	for _, test := range cases {
		if !t.Run(test.name, func(t *testing.T) {
			runTrackCompositeFileTest(t, conf, p, test)
		}) {
			t.FailNow()
		}
	}

	require.NoError(t, room.LocalParticipant.UnpublishTrack(p.audioTrackID))
	require.NoError(t, room.LocalParticipant.UnpublishTrack(p.videoTrackID))
}

func runTrackCompositeFileTest(t *testing.T, conf *config.Config, params *sdkParams, test *testCase) {
	filepath, filename := getFileInfo(conf, test, "tc")

	var audioTrackID, videoTrackID string
	if !test.videoOnly {
		audioTrackID = params.audioTrackID
	}
	if !test.audioOnly {
		videoTrackID = params.videoTrackID
	}

	trackRequest := &livekit.TrackCompositeEgressRequest{
		RoomName:     params.roomName,
		AudioTrackId: audioTrackID,
		VideoTrackId: videoTrackID,
		Output: &livekit.TrackCompositeEgressRequest_File{
			File: &livekit.EncodedFileOutput{
				FileType: test.fileType,
				Filepath: filepath,
			},
		},
	}

	if test.options != nil {
		trackRequest.Options = &livekit.TrackCompositeEgressRequest_Advanced{
			Advanced: test.options,
		}
	}

	req := &livekit.StartEgressRequest{
		EgressId:  utils.NewGuid(utils.EgressPrefix),
		RequestId: utils.NewGuid(utils.RPCPrefix),
		SentAt:    time.Now().UnixNano(),
		Request: &livekit.StartEgressRequest_TrackComposite{
			TrackComposite: trackRequest,
		},
	}

	runFileTest(t, conf, test, req, filename)
}

func testTrack(t *testing.T, conf *config.Config, room *lksdk.Room) {
	for _, test := range []*testCase{
		{
			audioOnly:     true,
			codec:         "opus",
			fileExtension: "ogg",
		},
		{
			videoOnly:     true,
			codec:         "vp8",
			fileExtension: "ivf",
		},
		{
			videoOnly:     true,
			codec:         "h264",
			fileExtension: "h264",
		},
	} {
		test.filePrefix = fmt.Sprintf("track-%s", test.codec)

		if !t.Run(test.filePrefix, func(t *testing.T) {
			runTrackFileTest(t, conf, room, test)
		}) {
			t.FailNow()
		}
	}
}

func runTrackFileTest(t *testing.T, conf *config.Config, room *lksdk.Room, test *testCase) {
	var trackID string
	if test.audioOnly {
		p := publishSamplesToRoom(t, room, test.codec, "")
		trackID = p.audioTrackID
	} else {
		p := publishSamplesToRoom(t, room, "", test.codec)
		trackID = p.videoTrackID
	}
	time.Sleep(time.Second)

	filepath, filename := getFileInfo(conf, test, "track")

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

	runFileTest(t, conf, test, req, filename)

	require.NoError(t, room.LocalParticipant.UnpublishTrack(trackID))
	time.Sleep(time.Second)
}

func testTrackWebSocket(t *testing.T, conf *config.Config, room *lksdk.Room) {
	for _, test := range []*testCase{
		{
			audioOnly:     true,
			codec:         "opus",
			fileExtension: "ogg",
		},
		//{
		//
		//	videoOnly:     true,
		//	codec:         "vp8",
		//	fileExtension: "ivf",
		//},
		//{
		//	videoOnly:     true,
		//	codec:         "h264",
		//	fileExtension: "h264",
		//},
	} {
		test.filePrefix = fmt.Sprintf("track_ws-%s", test.codec)

		if !t.Run(test.filePrefix, func(t *testing.T) {
			runTrackWebSocketTest(t, conf, room, test)
		}) {
			// t.FailNow()
		}
	}
}

type testWebSocketServer struct {
	path string
	file *os.File
	conn *websocket.Conn
	done chan struct{}
}

func newTestWebsocketServer(filepath string) *testWebSocketServer {
	return &testWebSocketServer{
		path: filepath,
		done: make(chan struct{}),
	}
}

func (s *testWebSocketServer) handleWebsocket(w http.ResponseWriter, r *http.Request) {
	// Determine file type
	ct := r.Header.Get("Content-Type")

	var err error
	var upgrader = websocket.Upgrader{}

	switch {
	case strings.EqualFold(ct, "video/vp8"):
		s.file, err = os.Create(s.path)
	case strings.EqualFold(ct, "video/h264"):
		s.file, err = os.Create(s.path)
	case strings.EqualFold(ct, "audio/opus"):
		s.file, err = os.Create(s.path)
	default:
		log.Fatal("Unsupported codec")
		return
	}
	if err != nil {
		log.Fatalf("Error in creating file: %s\n", err)
		return
	}

	// Try accepting the WS connection
	s.conn, err = upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Fatalf("Error in accepting WS connection: %s\n", err)
		return
	}

	log.Println("Websocket connection received!")

	go func() {
		defer func() {
			s.file.Close()

			// Close the connection when we don't have unexpected EOF;
			// we can't close a socket connection that's unexpectedly closed
			if !websocket.IsUnexpectedCloseError(err) {
				s.conn.Close()
			}
		}()

		for {
			select {
			case <-s.done:
				return
			default:
				mt, msg, err := s.conn.ReadMessage()
				if err != nil {
					log.Printf("Error in reading message: %s\n", err)
					return
				}

				switch mt {
				case websocket.BinaryMessage:
					if s.file == nil {
						log.Printf("File is not open")
						return
					}
					_, err = s.file.Write(msg)
					if err != nil {
						log.Printf("Error while writing to file: %s\n", err)
						return
					}
				}
			}
		}
	}()
}

func (s *testWebSocketServer) close() {
	close(s.done)
}

func runTrackWebSocketTest(t *testing.T, conf *config.Config, room *lksdk.Room, test *testCase) {

	var trackID string
	if test.audioOnly {
		p := publishSamplesToRoom(t, room, test.codec, "")
		trackID = p.audioTrackID
	} else {
		p := publishSamplesToRoom(t, room, "", test.codec)
		trackID = p.videoTrackID
	}

	time.Sleep(time.Second * 5)

	_, filename := getFileInfo(conf, test, "track_ws")
	wss := newTestWebsocketServer(filename)
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

	runWebSocketTest(t, conf, test, req, filename)

	require.NoError(t, room.LocalParticipant.UnpublishTrack(trackID))
}
