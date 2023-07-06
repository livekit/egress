package sink

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

const (
	pingPeriod  = time.Second * 30
	readTimeout = time.Minute
)

type WebsocketSink struct {
	mu     sync.Mutex
	conn   *websocket.Conn
	closed atomic.Bool
}

func newWebsocketSink(o *config.StreamConfig, mimeType types.MimeType) (*WebsocketSink, error) {
	// set Content-Type header
	header := http.Header{}
	header.Set("Content-Type", string(mimeType))

	conn, _, err := websocket.DefaultDialer.Dial(o.Urls[0], header)
	if err != nil {
		return nil, err
	}
	return &WebsocketSink{
		conn: conn,
	}, nil
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
		for {
			_ = s.conn.SetReadDeadline(time.Now().Add(readTimeout))
			_, _, _ = s.conn.ReadMessage()
			if s.closed.Load() {
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
		return 0, errors.ErrWebsocketClosed(s.conn.RemoteAddr().String())
	}

	return len(p), s.conn.WriteMessage(websocket.BinaryMessage, p)
}

func (s *WebsocketSink) OnTrackMuted(muted bool) {
	err := s.writeMutedMessage(muted)
	if err != nil {
		logger.Errorw("failed to write muted message", err)
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
		return errors.ErrWebsocketClosed(s.conn.RemoteAddr().String())
	}

	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *WebsocketSink) Finalize() error {
	return s.Close()
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
