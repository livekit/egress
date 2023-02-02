package sink

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gorilla/websocket"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

type websocketState string

const (
	WebSocketActive websocketState = "active"
	WebSocketClosed websocketState = "closed"
)

type WebsocketSink struct {
	conn   *websocket.Conn
	closed chan struct{}
	state  websocketState
}

func newWebSocketSink(url string, mimeType types.MimeType) (*WebsocketSink, error) {
	// set Content-Type header
	header := http.Header{}
	header.Set("Content-Type", string(mimeType))

	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		return nil, err
	}

	s := &WebsocketSink{
		conn:   conn,
		closed: make(chan struct{}),
		state:  WebSocketActive,
	}

	return s, nil
}

func (s *WebsocketSink) Start() error {
	return nil
}

func (s *WebsocketSink) Write(p []byte) (n int, err error) {
	if s.state == WebSocketClosed {
		return 0, errors.ErrWebSocketClosed(s.conn.RemoteAddr().String())
	}

	return len(p), s.conn.WriteMessage(websocket.BinaryMessage, p)
}

func (s *WebsocketSink) TrackMuted(muted bool) {
	err := s.writeMutedMessage(muted)
	if err != nil {
		logger.Errorw("failed to write muted message", err)
	}
}

type textMessagePayload struct {
	Muted bool `json:"muted"`
}

func (s *WebsocketSink) writeMutedMessage(muted bool) error {
	// If the socket is closed, return error
	if s.state == WebSocketClosed {
		return errors.ErrWebSocketClosed(s.conn.RemoteAddr().String())
	}

	// Marshal `muted` payload
	data, err := json.Marshal(&textMessagePayload{
		Muted: muted,
	})
	if err != nil {
		return err
	}

	// Write message
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *WebsocketSink) Close() error {
	if s.state == WebSocketClosed {
		return nil
	}

	// write close message for graceful disconnection
	err := s.conn.WriteMessage(websocket.CloseMessage, nil)
	if err != nil && !errors.Is(err, io.EOF) {
		logger.Errorw("cannot write WS close message", err)
	}

	// terminate connection and close the `closed` channel
	err = s.conn.Close()
	close(s.closed)
	s.state = WebSocketClosed
	return err
}

func (s *WebsocketSink) Cleanup() {
	return
}
