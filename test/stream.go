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
	"context"
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
	streamKey3          = utils.NewGuid("")
	streamKey4          = utils.NewGuid("")
	rtmpUrl1            = fmt.Sprintf("rtmp://localhost:1935/live/%s", streamKey1)
	rtmpUrl2            = fmt.Sprintf("rtmp://localhost:1935/live/%s", streamKey2)
	rtmpUrl3            = fmt.Sprintf("rtmp://localhost:1935/live/%s", streamKey3)
	rtmpUrl4            = fmt.Sprintf("rtmp://localhost:1935/live/%s", streamKey4)
	rtmpUrl1Redacted, _ = utils.RedactStreamKey(rtmpUrl1)
	rtmpUrl2Redacted, _ = utils.RedactStreamKey(rtmpUrl2)
	rtmpUrl3Redacted, _ = utils.RedactStreamKey(rtmpUrl3)
	rtmpUrl4Redacted, _ = utils.RedactStreamKey(rtmpUrl4)
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
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeVP8,
					layout:     "speaker",
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
					audioCodec: types.MimeTypeOpus,
					videoCodec: types.MimeTypeVP8,
					layout:     "speaker",
				},
				streamOptions: &streamOptions{
					streamUrls: []string{rtmpUrl1, badRtmpUrl1},
					outputType: types.OutputTypeRTMP,
				},
				encodingOptions: &livekit.EncodingOptions{
					KeyFrameInterval: 2,
				},
				contentCheck: r.streamKeyframeContentCheck(2),
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
			},
		} {
			if !r.run(t, test, r.runStreamTest) {
				return
			}
		}
	})
}

func (r *Runner) runStreamTest(t *testing.T, test *testCase) {
	if test.requestType == types.RequestTypeTrack {
		r.runWebsocketTest(t, test)
		return
	}

	req := r.build(test)

	ctx := context.Background()
	urls := streamUrls[test.streamOptions.outputType]
	egressID := r.startEgress(t, req)

	p, err := config.GetValidatedPipelineConfig(r.ServiceConfig, req)
	require.NoError(t, err)

	if !test.audioOnly {
		require.True(t, p.VideoEncoding)
	}

	// verify
	time.Sleep(time.Second * 5)
	r.verifyStreams(t, test, p, urls[0][2])
	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		urls[0][1]: livekit.StreamInfo_ACTIVE,
		urls[1][1]: livekit.StreamInfo_FAILED,
	})

	// add one good stream url and one bad
	_, err = r.client.UpdateStream(ctx, egressID, &livekit.UpdateStreamRequest{
		EgressId:      egressID,
		AddOutputUrls: []string{urls[2][0], urls[3][0]},
	})
	require.NoError(t, err)
	time.Sleep(time.Second * 5)

	// verify
	r.verifyStreams(t, test, p, urls[0][2], urls[2][2])
	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		urls[0][1]: livekit.StreamInfo_ACTIVE,
		urls[1][1]: livekit.StreamInfo_FAILED,
		urls[2][1]: livekit.StreamInfo_ACTIVE,
		urls[3][1]: livekit.StreamInfo_FAILED,
	})

	// remove one of the stream urls
	_, err = r.client.UpdateStream(ctx, egressID, &livekit.UpdateStreamRequest{
		EgressId:         egressID,
		RemoveOutputUrls: []string{urls[0][0]},
	})
	require.NoError(t, err)

	time.Sleep(time.Second * 5)
	if r.Dotfiles {
		r.createDotFile(t, egressID)
	}

	// verify the remaining stream
	r.verifyStreams(t, test, p, urls[2][2])
	r.checkStreamUpdate(t, egressID, map[string]livekit.StreamInfo_Status{
		urls[0][1]: livekit.StreamInfo_FINISHED,
		urls[1][1]: livekit.StreamInfo_FAILED,
		urls[2][1]: livekit.StreamInfo_ACTIVE,
		urls[3][1]: livekit.StreamInfo_FAILED,
	})

	// stop
	time.Sleep(time.Second * 5)
	res := r.stopEgress(t, egressID)

	// verify egress info
	require.Empty(t, res.Error)
	require.NotZero(t, res.StartedAt)
	require.NotZero(t, res.EndedAt)

	// check stream info
	require.Len(t, res.StreamResults, 4)
	for _, info := range res.StreamResults {
		require.NotZero(t, info.StartedAt)
		require.NotZero(t, info.EndedAt)

		switch info.Url {
		case urls[0][1]:
			require.Equal(t, livekit.StreamInfo_FINISHED.String(), info.Status.String())
			require.Greater(t, float64(info.Duration)/1e9, 15.0)

		case urls[2][1]:
			require.Equal(t, livekit.StreamInfo_FINISHED.String(), info.Status.String())
			require.Greater(t, float64(info.Duration)/1e9, 10.0)

		default:
			require.Equal(t, livekit.StreamInfo_FAILED.String(), info.Status.String())
		}
	}
}

func (r *Runner) verifyStreams(t *testing.T, tc *testCase, p *config.PipelineConfig, urls ...string) {
	for _, url := range urls {
		info := verify(t, url, p, nil, types.EgressTypeStream, false, r.sourceFramerate, false)
		if tc != nil && tc.contentCheck != nil && info != nil {
			tc.contentCheck(t, url, info)
		}
	}
}

func (r *Runner) runWebsocketTest(t *testing.T, test *testCase) {
	filepath := path.Join(r.FilePrefix, test.streamOptions.rawFileName)
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
	verify(t, filepath, p, res, types.EgressTypeWebsocket, r.Muting, r.sourceFramerate, false)
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
