package sink

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

const pingPeriod = time.Second * 30

type WebsocketSink struct {
	mu      sync.Mutex
	conn    *websocket.Conn
	closed  atomic.Bool
	errChan chan error
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
		conn:    conn,
		errChan: make(chan error, 1),
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
		errCount := 0
		for {
			_, _, err := s.conn.ReadMessage()
			if s.closed.Load() {
				return
			}
			if err != nil {
				_, isCloseError := err.(*websocket.CloseError)
				if isCloseError || errors.Is(err, io.EOF) || strings.HasSuffix(err.Error(), "use of closed network connection") {
					s.errChan <- err
					s.closed.Store(true)
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
		select {
		case err := <-s.errChan:
			return 0, err
		default:
			return 0, errors.ErrWebsocketClosed(s.conn.RemoteAddr().String())
		}
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
	logger.Debugw("closing websocket sink")

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
