package output

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gorilla/websocket"

	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
)

type websocketState string

const (
	WebSocketActive websocketState = "active"
	WebSocketClosed websocketState = "closed"
)

type websocketSink struct {
	conn   *websocket.Conn
	logger logger.Logger
	muted  chan bool
	closed chan struct{}
	state  websocketState
}

func newWebSocketSink(url string, mimeType params.MimeType, logger logger.Logger, muted chan bool) (io.WriteCloser, error) {
	// set Content-Type header
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

func (s *websocketSink) Write(p []byte) (n int, err error) {
	if s.state == WebSocketClosed {
		return 0, errors.ErrWebSocketClosed(s.conn.RemoteAddr().String())
	}

	return len(p), s.conn.WriteMessage(websocket.BinaryMessage, p)
}

func (s *websocketSink) Close() error {
	if s.state == WebSocketClosed {
		return nil
	}

	// write close message for graceful disconnection
	err := s.conn.WriteMessage(websocket.CloseMessage, nil)
	if err != nil && !errors.Is(err, io.EOF) {
		s.logger.Errorw("cannot write WS close message", err)
	}

	// terminate connection and close the `closed` channel
	err = s.conn.Close()
	close(s.closed)
	s.state = WebSocketClosed
	return err
}

type textMessagePayload struct {
	Muted bool `json:"muted"`
}

func (s *websocketSink) writeMutedMessage(muted bool) error {
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
			if err != nil && !errors.Is(err, io.EOF) {
				s.logger.Errorw("error writing muted message: ", err)
			}
		case <-s.closed:
			return
		}
	}
}
