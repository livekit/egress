package sink

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/frostbyte73/core"
	"github.com/gorilla/websocket"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

type websocketState string

const (
	WebsocketActive websocketState = "active"
	WebsocketClosed websocketState = "closed"
)

type WebsocketSink struct {
	conn   *websocket.Conn
	closed core.Fuse
	state  websocketState
}

func newWebsocketSink(url string, mimeType types.MimeType) (*WebsocketSink, error) {
	// set Content-Type header
	header := http.Header{}
	header.Set("Content-Type", string(mimeType))

	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		return nil, err
	}

	s := &WebsocketSink{
		conn:   conn,
		closed: core.NewFuse(),
		state:  WebsocketActive,
	}

	return s, nil
}

func (s *WebsocketSink) Start() error {
	return nil
}

func (s *WebsocketSink) Write(p []byte) (n int, err error) {
	if s.state == WebsocketClosed {
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
	// If the socket is closed, return error
	if s.state == WebsocketClosed {
		return errors.ErrWebsocketClosed(s.conn.RemoteAddr().String())
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

func (s *WebsocketSink) Finalize() error {
	return s.Close()
}

func (s *WebsocketSink) Close() error {
	if s.state == WebsocketClosed {
		return nil
	}

	// write close message for graceful disconnection
	err := s.conn.WriteMessage(websocket.CloseMessage, nil)
	if err != nil && !errors.Is(err, io.EOF) {
		logger.Errorw("cannot write WS close message", err)
	}

	// terminate connection and close the `closed` channel
	err = s.conn.Close()
	s.closed.Break()
	s.state = WebsocketClosed
	return err
}

func (s *WebsocketSink) Cleanup() {
	return
}
