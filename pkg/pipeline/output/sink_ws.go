package output

import (
	"github.com/gorilla/websocket"
	"github.com/livekit/protocol/logger"
	"io"
	"net/http"
)

type websocketSink struct {
	conn   *websocket.Conn
	logger logger.Logger
}

func (s *websocketSink) Write(p []byte) (n int, err error) {
	//websocket.IsUnexpectedCloseError()
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

func newWebSocketSink(url string, mimeType string, logger logger.Logger) (io.WriteCloser, error) {
	// Set Content-Type in header
	header := http.Header{}
	header.Set("Content-Type", mimeType)

	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		return nil, err
	}

	return &websocketSink{conn, logger}, nil
}
