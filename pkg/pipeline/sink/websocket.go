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

	s := &WebsocketSink{
		conn: conn,
	}
	go s.keepAlive()

	return s, nil
}

func (s *WebsocketSink) Start() error {
	return nil
}

func (s *WebsocketSink) keepAlive() {
	ticker := time.NewTicker(time.Second * 10)
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
