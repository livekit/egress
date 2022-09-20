package output

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/logger"
)

func buildWebsocketOutputBin(p *params.Params) (*OutputBin, error) {
	writer, err := newWebSocketSink(p.WebsocketUrl, params.MimeTypeRaw, p.Logger, p.MutedChan)
	if err != nil {
		return nil, err
	}

	sink, err := app.NewAppSink()
	if err != nil {
		return nil, err
	}

	sink.SetCallbacks(&app.SinkCallbacks{
		EOSFunc: func(appSink *app.Sink) {
			// Close writer on EOS
			if err = writer.Close(); err != nil && !errors.Is(err, io.EOF) {
				p.Logger.Errorw("cannot close WS sink", err)
			}
		},
		NewSampleFunc: func(appSink *app.Sink) gst.FlowReturn {
			// Pull the sample that triggered this callback
			sample := appSink.PullSample()
			if sample == nil {
				return gst.FlowEOS
			}

			// Retrieve the buffer from the sample
			buffer := sample.GetBuffer()
			if buffer == nil {
				return gst.FlowError
			}

			// Map the buffer to READ operation
			samples := buffer.Map(gst.MapRead).Bytes()

			// From the extracted bytes, send to writer
			_, err = writer.Write(samples)
			if err != nil && !errors.Is(err, io.EOF) {
				p.Logger.Errorw("cannot read AppSink samples", err)
				return gst.FlowError
			}
			return gst.FlowOK
		},
	})

	bin := gst.NewBin("output")
	if err = bin.Add(sink.Element); err != nil {
		return nil, err
	}

	ghostPad := gst.NewGhostPad("sink", sink.GetStaticPad("sink"))
	if !bin.AddPad(ghostPad.Pad) {
		return nil, errors.ErrGhostPadFailed
	}

	return &OutputBin{
		bin:    bin,
		logger: p.Logger,
	}, nil
}

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
