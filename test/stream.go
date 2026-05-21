// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
)

const (
	badRtmpUrl1         = "rtmp://localhost:1936/wrong/stream"
	badRtmpUrl1Redacted = "rtmp://localhost:1936/wrong/{st...am}"
	badRtmpUrl2         = "rtmp://localhost:1936/live/stream"
	badRtmpUrl2Redacted = "rtmp://localhost:1936/live/{st...am}"
	badSrtUrl1          = "srt://localhost:8891?streamid=publish:wrongport&pkt_size=1316"
	badSrtUrl2          = "srt://localhost:8891?streamid=publish:badstream&pkt_size=1316"
)

var (
	streamKey1          = utils.NewGuid("")
	streamKey2          = utils.NewGuid("")
	rtmpUrl1            = fmt.Sprintf("rtmp://localhost:1935/live/%s", streamKey1)
	rtmpUrl2            = fmt.Sprintf("rtmp://localhost:1935/live/%s", streamKey2)
	rtmpUrl1Redacted, _ = utils.RedactStreamKey(rtmpUrl1)
	rtmpUrl2Redacted, _ = utils.RedactStreamKey(rtmpUrl2)
	srtPublishUrl1      = fmt.Sprintf("srt://localhost:8890?streamid=publish:%s&pkt_size=1316", streamKey1)
	srtReadUrl1         = fmt.Sprintf("srt://localhost:8890?streamid=read:%s", streamKey1)
	srtPublishUrl2      = fmt.Sprintf("srt://localhost:8890?streamid=publish:%s&pkt_size=1316", streamKey2)
	srtReadUrl2         = fmt.Sprintf("srt://localhost:8890?streamid=read:%s", streamKey2)
)

// [[publish, redacted, verification]]
var streamUrls = map[types.OutputType][][]string{
	types.OutputTypeRTMP: {
		{rtmpUrl1, rtmpUrl1Redacted, rtmpUrl1},
		{badRtmpUrl1, badRtmpUrl1Redacted, ""},
		{rtmpUrl2, rtmpUrl2Redacted, rtmpUrl2},
		{badRtmpUrl2, badRtmpUrl2Redacted, ""},
	},
	types.OutputTypeSRT: {
		{srtPublishUrl1, srtPublishUrl1, srtReadUrl1},
		{badSrtUrl1, badSrtUrl1, ""},
		{srtPublishUrl2, srtPublishUrl2, srtReadUrl2},
		{badSrtUrl2, badSrtUrl2, ""},
	},
}

func (r *Runner) testStream(t *testing.T) {
	if !r.should(runStream) {
		return
	}

	t.Run("Stream", func(t *testing.T) {
		for _, test := range []*testCase{

			// ---- Room Composite -----

			{
				name:        "RoomComposite",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec:       types.MimeTypeOpus,
					videoCodec:       types.MimeTypeVP8,
					layout:           layoutSpeaker,
					multiParticipant: true,
				},
				streamOptions: &streamOptions{
					streamUrls: []string{rtmpUrl1, badRtmpUrl1},
					outputType: types.OutputTypeRTMP,
				},
			},
			{
				name:        "RoomCompositeFixedKeyframeInterval",
				requestType: types.RequestTypeRoomComposite,
				publishOptions: publishOptions{
					audioCodec:       types.MimeTypeOpus,
					videoCodec:       types.MimeTypeVP8,
					layout:           layoutSpeaker,
					multiParticipant: true,
				},
				streamOptions: &streamOptions{
					streamUrls: []string{rtmpUrl1, badRtmpUrl1},
					outputType: types.OutputTypeRTMP,
				},
				encodingOptions: &livekit.EncodingOptions{
					KeyFrameInterval: 2,
				},
				contentCheck: streamKeyframeContentCheck(2),
			},

			// ---------- Web ----------

			{
				name:        "Web",
				requestType: types.RequestTypeWeb,
				streamOptions: &streamOptions{
					streamUrls: []string{srtPublishUrl1, badSrtUrl1},
					outputType: types.OutputTypeSRT,
				},
				encodingOptions: &livekit.EncodingOptions{
					KeyFrameInterval: 2,
				},
			},

			// ------ Participant ------

			{
				name:        "ParticipantComposite",
				requestType: types.RequestTypeParticipant,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					audioDelay: time.Second * 8,
					videoCodec: types.MimeTypeVP8,
				},
				streamOptions: &streamOptions{
					streamUrls: []string{rtmpUrl1, badRtmpUrl1},
					outputType: types.OutputTypeRTMP,
				},
			},

			// ---- Track Composite ----

			{
				name:        "TrackComposite",
				requestType: types.RequestTypeTrackComposite,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeVP8,
				},
				streamOptions: &streamOptions{
					streamUrls: []string{rtmpUrl1, badRtmpUrl1},
					outputType: types.OutputTypeRTMP,
				},
			},

			// --------- Track ---------

			{
				name:        "Track",
				requestType: types.RequestTypeTrack,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					audioOnly:  true,
				},
				streamOptions: &streamOptions{
					rawFileName: fmt.Sprintf("track-ws-%v.raw", time.Now().Unix()),
					outputType:  types.OutputTypeRaw,
				},
				custom: r.runWebsocketTest,
			},

			// -------- Template --------

			{
				name:        "Template",
				requestType: types.RequestTypeTemplate,
				publishOptions: publishOptions{
					audioCodec:       types.MimeTypeOpus,
					videoCodec:       types.MimeTypeVP8,
					layout:           layoutSpeaker,
					multiParticipant: true,
				},
				streamOptions: &streamOptions{
					streamUrls: []string{rtmpUrl1, badRtmpUrl1},
					outputType: types.OutputTypeRTMP,
				},
			},

			// -------- Media ----------

			{
				name:        "Media/ParticipantVideoStream",
				requestType: types.RequestTypeMedia,
				publishOptions: publishOptions{
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeVP8,
					mediaParticipantVideo: &livekit.ParticipantVideo{
						Identity: "set-at-runtime",
					},
					audioRoutes: []*livekit.AudioRoute{{
						Match: &livekit.AudioRoute_TrackId{TrackId: "set-at-runtime"},
					}},
				},
				streamOptions: &streamOptions{
					streamUrls: []string{rtmpUrl1, badRtmpUrl1},
					outputType: types.OutputTypeRTMP,
				},
			},
		} {
			if !r.run(t, test) {
				return
			}
		}
	})
}

func (r *Runner) verifyStreams(t *testing.T, tc *testCase, p *config.PipelineConfig, urls ...string) {
	for _, url := range urls {
		info := verify(t, url, p, nil, types.EgressTypeStream, r.sourceFramerate, false)
		if tc != nil && tc.contentCheck != nil {
			runContentCheck(t, tc, url, info, "stream", formatFromStreamURL(url))
		}
	}
}

func (r *Runner) runWebsocketTest(t *testing.T, test *testCase) {
	filepath := path.Join(r.FilePrefix, test.rawFileName)
	wss := newTestWebsocketServer(filepath)
	s := httptest.NewServer(http.HandlerFunc(wss.handleWebsocket))
	test.websocketUrl = "ws" + strings.TrimPrefix(s.URL, "http")
	defer func() {
		wss.close()
		s.Close()
	}()

	req := r.build(test)

	egressID := r.startEgress(t, req)

	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	time.Sleep(time.Second * 30)

	res := r.stopEgress(t, egressID)
	verify(t, filepath, p, res, types.EgressTypeWebsocket, r.sourceFramerate, false)
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
