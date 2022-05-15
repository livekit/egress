//go:build integration
// +build integration

package test

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/livekit/livekit-egress/pkg/pipeline"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go"

	"github.com/livekit/livekit-egress/pkg/config"
)

func testTrackComposite(t *testing.T, conf *config.Config, room *lksdk.Room) {
	testTrackCompositeFile(t, conf, room, params.MimeTypeOpus, params.MimeTypeVP8, []*testCase{
		{
			name:     "tc-vp8-mp4",
			fileType: livekit.EncodedFileType_MP4,
			filename: fmt.Sprintf("tc-vp8-%v.mp4", time.Now().Unix()),
		},
		{
			name:      "tc-opus-ogg",
			audioOnly: true,
			fileType:  livekit.EncodedFileType_OGG,
			filename:  fmt.Sprintf("tc-opus-%v.ogg", time.Now().Unix()),
		},
	})

	testTrackCompositeFile(t, conf, room, params.MimeTypeOpus, params.MimeTypeH264, []*testCase{
		{
			name:     "tc-h264-mp4",
			fileType: livekit.EncodedFileType_MP4,
			filename: fmt.Sprintf("tc-h264-%v.mp4", time.Now().Unix()),
		},
		{
			name:      "tc-h264-only-mp4",
			videoOnly: true,
			fileType:  livekit.EncodedFileType_MP4,
			filename:  fmt.Sprintf("tc-h264-only-%v.mp4", time.Now().Unix()),
		},
	})
}

func testTrackCompositeFile(t *testing.T, conf *config.Config, room *lksdk.Room, audioCodec, videoCodec params.MimeType, cases []*testCase) {
	p := publishSamplesToRoom(t, room, audioCodec, videoCodec, false)

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
	var audioTrackID, videoTrackID string
	if !test.videoOnly {
		audioTrackID = params.audioTrackID
	}
	if !test.audioOnly {
		videoTrackID = params.videoTrackID
	}

	filepath := getFilePath(conf, test.filename)
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

	runFileTest(t, conf, test, req, filepath)
}

func testTrackFile(t *testing.T, conf *config.Config, room *lksdk.Room) {
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

func runTrackFileTest(t *testing.T, conf *config.Config, room *lksdk.Room, test *testCase) {
	var trackID string
	if test.audioOnly {
		p := publishSamplesToRoom(t, room, test.codec, "", false)
		trackID = p.audioTrackID
	} else {
		p := publishSamplesToRoom(t, room, "", test.codec, false)
		trackID = p.videoTrackID
	}
	time.Sleep(time.Second)

	filepath := getFilePath(conf, test.filename)
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

	require.NoError(t, room.LocalParticipant.UnpublishTrack(trackID))
	time.Sleep(time.Second)
}

func testTrackWebsocket(t *testing.T, conf *config.Config, room *lksdk.Room) {
	for _, test := range []*testCase{
		{
			name:      "track-websocket",
			audioOnly: true,
			codec:     params.MimeTypeOpus,
			output:    params.OutputTypeRaw,
			filename:  fmt.Sprintf("track_ws-%v.raw", time.Now().Unix()),
		},
		//{
		//	name:      "track-websocket",
		//	audioOnly: true,
		//	codec:     params.MimeTypeOpus,
		//	output:    params.OutputTypeOGG,
		//	filename:  fmt.Sprintf("track_ws-%v.ogg", time.Now().Unix()),
		//},
	} {
		if !t.Run(test.name, func(t *testing.T) {
			runTrackWebsocketTest(t, conf, room, test)
		}) {
			t.FailNow()
		}
	}
}

func runTrackWebsocketTest(t *testing.T, conf *config.Config, room *lksdk.Room, test *testCase) {
	var trackID string
	if test.audioOnly {
		p := publishSamplesToRoom(t, room, test.codec, "", false)
		trackID = p.audioTrackID
	} else {
		p := publishSamplesToRoom(t, room, "", test.codec, false)
		trackID = p.videoTrackID
	}

	time.Sleep(time.Second * 5)

	filepath := getFilePath(conf, test.filename)
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

	p, err := params.GetPipelineParams(conf, req)
	require.NoError(t, err)

	rec, err := pipeline.New(conf, p)
	require.NoError(t, err)

	// record for ~30s. Takes about 5s to start
	time.AfterFunc(time.Second*35, func() {
		rec.Stop()
	})
	res := rec.Run()

	require.NoError(t, room.LocalParticipant.UnpublishTrack(trackID))
	verify(t, filepath, p, res, false)
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
	var upgrader = websocket.Upgrader{}

	// Determine file type
	ct := r.Header.Get("Content-Type")

	switch {
	case strings.EqualFold(ct, "video/vp8"):
		s.file, err = os.Create(s.path)
	case strings.EqualFold(ct, "video/h264"):
		s.file, err = os.Create(s.path)
	case strings.EqualFold(ct, "audio/opus"):
		s.file, err = os.Create(s.path)
	default:
		log.Fatal("Unsupported codec: ", ct)
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

			// Close the connection only if it's not closed already
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
					if !websocket.IsUnexpectedCloseError(err) {
						log.Printf("Error in reading message: %s\n", err)
					}
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

func (s *websocketTestServer) close() {
	close(s.done)
}
