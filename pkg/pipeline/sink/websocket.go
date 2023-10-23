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

package sink

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/gorilla/websocket"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

const pingPeriod = time.Second * 30

type WebsocketSink struct {
	mu            sync.Mutex
	conn          *websocket.Conn
	sinkCallbacks *app.SinkCallbacks
	closed        atomic.Bool
}

func newWebsocketSink(o *config.StreamConfig, mimeType types.MimeType, callbacks *gstreamer.Callbacks) (*WebsocketSink, error) {
	// set Content-Type header
	header := http.Header{}
	header.Set("Content-Type", string(mimeType))

	conn, _, err := websocket.DefaultDialer.Dial(o.Urls[0], header)
	if err != nil {
		return nil, err
	}

	s := &WebsocketSink{
		conn: conn,
	}
	s.sinkCallbacks = &app.SinkCallbacks{
		EOSFunc: func(appSink *app.Sink) {
			_ = s.Close()
		},
		NewSampleFunc: func(appSink *app.Sink) gst.FlowReturn {
			// pull the sample that triggered this callback
			sample := appSink.PullSample()
			if sample == nil {
				return gst.FlowOK
			}

			// retrieve the buffer from the sample
			buffer := sample.GetBuffer()
			if buffer == nil {
				return gst.FlowOK
			}

			// map the buffer to READ operation
			samples := buffer.Map(gst.MapRead).Bytes()

			// send to writer
			_, err = s.Write(samples)
			if err != nil {
				if err == io.EOF {
					return gst.FlowEOS
				}
				callbacks.OnError(err)
			}

			return gst.FlowOK
		},
	}
	callbacks.AddOnTrackMuted(s.OnTrackMuted)
	callbacks.AddOnTrackUnmuted(s.OnTrackUnmuted)

	return s, nil
}

func (s *WebsocketSink) SinkCallbacks() *app.SinkCallbacks {
	return s.sinkCallbacks
}

func (s *WebsocketSink) Start() error {
	// override default ping handler to include locking
	s.conn.SetPingHandler(func(_ string) error {
		s.mu.Lock()
		defer s.mu.Unlock()

		_ = s.conn.WriteMessage(websocket.PongMessage, []byte("pong"))
		return nil
	})

	// read loop is required for the ping handler to receive pings
	go func() {
		errCount := 0
		for {
			_, _, err := s.conn.ReadMessage()
			if s.closed.Load() {
				return
			}
			if err != nil {
				var closeError *websocket.CloseError
				if errors.As(err, &closeError) ||
					errors.Is(err, io.EOF) ||
					strings.HasSuffix(err.Error(), "use of closed network connection") {
					return
				}
				errCount++
			}
			// reads will panic after 1000 errors, break loop before that happens
			if errCount > 100 {
				logger.Errorw("closing websocket reader", err)
				return
			}
		}
	}()

	// write loop for sending pings
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()

		for {
			<-ticker.C
			s.mu.Lock()
			if s.closed.Load() {
				s.mu.Unlock()
				return
			}
			_ = s.conn.WriteMessage(websocket.PingMessage, []byte("ping"))
			s.mu.Unlock()
		}
	}()

	return nil
}

func (s *WebsocketSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return 0, io.EOF
	}

	return len(p), s.conn.WriteMessage(websocket.BinaryMessage, p)
}

func (s *WebsocketSink) OnTrackMuted(_ string) {
	if err := s.writeMutedMessage(true); err != nil {
		logger.Errorw("failed to write mute message", err)
	}
}

func (s *WebsocketSink) OnTrackUnmuted(_ string, _ time.Duration) {
	if err := s.writeMutedMessage(false); err != nil {
		logger.Errorw("failed to write unmute message", err)
	}
}

type textMessagePayload struct {
	Muted bool `json:"muted"`
}

func (s *WebsocketSink) writeMutedMessage(muted bool) error {
	data, err := json.Marshal(&textMessagePayload{
		Muted: muted,
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return nil
	}

	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *WebsocketSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed.Swap(true) {
		// write close message for graceful disconnection
		_ = s.conn.WriteMessage(websocket.CloseMessage, nil)

		// terminate connection and close the `closed` channel
		return s.conn.Close()
	}

	return nil
}

func (s *WebsocketSink) Cleanup() {}
