package output

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gorilla/websocket"

	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/protocol/logger"
)

type websocketSink struct {
	conn   *websocket.Conn
	logger logger.Logger
	muted  chan bool
	closed chan struct{}
}

func (s *websocketSink) Write(p []byte) (n int, err error) {
	return len(p), s.conn.WriteMessage(websocket.BinaryMessage, p)
}

func (s *websocketSink) Close() error {
	// Write close message for graceful disconnection
	err := s.conn.WriteMessage(websocket.CloseMessage, nil)
	if err != nil {
		s.logger.Errorw("Cannot write WS close message", err)
	}

	// Terminate connection
	return s.conn.Close()
}

type textMessagePayload struct {
	muted bool `json:"muted"`
}

func (s *websocketSink) writeMutedMessage(muted bool) error {
	close(s.closed)
	data, err := json.Marshal(&textMessagePayload{
		muted: muted,
	})
	if err != nil {
		return err
	}
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *websocketSink) listenToMutedChan() {
	if s.muted == nil {
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
		conn,
		logger,
		muted,
		make(chan struct{}),
	}
	go s.listenToMutedChan()

	return s, nil
}
