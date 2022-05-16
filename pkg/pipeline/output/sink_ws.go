package output

import (
	"encoding/json"
	"fmt"
	"github.com/livekit/livekit-egress/pkg/errors"
	"io"
	"net/http"

	"github.com/gorilla/websocket"

	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/protocol/logger"
)

type websocketState string

const (
	WebSocketActive websocketState = "active"
	WebSocketClosed websocketState = "closed"
)

func ErrWebSocketClosed(addr string) error {
	return errors.New(fmt.Sprintf("WS already closed: %s", addr))
}

type websocketSink struct {
	conn   *websocket.Conn
	logger logger.Logger
	muted  chan bool
	closed chan struct{}
	state  websocketState
}

func (s *websocketSink) Write(p []byte) (n int, err error) {
	if s.state == WebSocketClosed {
		return 0, ErrWebSocketClosed(s.conn.RemoteAddr().String())
	}
	return len(p), s.conn.WriteMessage(websocket.BinaryMessage, p)
}

func (s *websocketSink) Close() error {
	// If already closed, return nil
	if s.state == WebSocketClosed {
		return nil
	}

	// Write close message for graceful disconnection
	err := s.conn.WriteMessage(websocket.CloseMessage, nil)
	if err != nil {
		s.logger.Errorw("Cannot write WS close message", err)
	}

	// Terminate connection and close the `closed` channel
	err = s.conn.Close()
	close(s.closed)
	s.state = WebSocketClosed
	return err
}

type textMessagePayload struct {
	muted bool `json:"muted"`
}

func (s *websocketSink) writeMutedMessage(muted bool) error {
	// If the socket is closed, return error
	if s.state == WebSocketClosed {
		return ErrWebSocketClosed(s.conn.RemoteAddr().String())
	}

	// Marshal `muted` payload
	data, err := json.Marshal(&textMessagePayload{
		muted: muted,
	})
	if err != nil {
		return err
	}

	// Write message
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *websocketSink) listenToMutedChan() {
	// If the `muted` channel is nil or socket is closed,
	// cannot send message. Just return
	if s.muted == nil || s.state == WebSocketClosed {
		return
	}
	var err error
	for {
		select {
		case val := <-s.muted:
			err = s.writeMutedMessage(val)
			if err != nil {
				s.logger.Errorw("Error writing muted message: ", err)
			}
		case <-s.closed:
			return
		}
	}
}

func newWebSocketSink(url string, mimeType params.MimeType, logger logger.Logger, muted chan bool) (io.WriteCloser, error) {
	// Set Content-Type in header
	header := http.Header{}
	header.Set("Content-Type", string(mimeType))

	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		return nil, err
	}

	s := &websocketSink{
		conn:   conn,
		logger: logger,
		muted:  muted,
		closed: make(chan struct{}),
		state:  WebSocketActive,
	}
	go s.listenToMutedChan()

	return s, nil
}
