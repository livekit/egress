package logging

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/frostbyte73/core"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/logger"
)

const (
	channelSize     = 4096
	dropLogThrottle = 10 * time.Second
)

var sdkPrefixes = map[string]bool{
	"turnc": true, // turnc ERROR
	"ice E": true, // ice ERROR
	"pc ER": true, // pc ERROR
	"twcc_": true, // twcc_sender_interceptor ERROR
	"SDK 2": true, // SDK default logger (year-prefixed timestamp)
}

type HandlerLogger struct {
	ch          chan []byte
	done        core.Fuse
	dropped     atomic.Int64
	lastDropLog atomic.Int64 // unix nanos
	l           logger.Logger
}

func NewHandlerLogger(handlerID, egressID string) *HandlerLogger {
	h := &HandlerLogger{
		ch: make(chan []byte, channelSize),
		l: logger.GetLogger().WithValues(
			"handlerID", handlerID,
			"egressID", egressID,
		),
	}
	go h.drain()
	return h
}

func (h *HandlerLogger) Write(p []byte) (int, error) {
	cp := make([]byte, len(p))
	copy(cp, p)

	select {
	case h.ch <- cp:
	default:
		count := h.dropped.Inc()
		now := time.Now().UnixNano()
		last := h.lastDropLog.Load()
		if now-last >= int64(dropLogThrottle) {
			if h.lastDropLog.CompareAndSwap(last, now) {
				h.l.Warnw(fmt.Sprintf("handler logger dropped %d messages", count), nil)
				h.dropped.Store(0)
			}
		}
	}

	return len(p), nil
}

func (h *HandlerLogger) Close() error {
	close(h.ch)
	<-h.done.Watch()
	return nil
}

func (h *HandlerLogger) drain() {
	var buf []byte

	defer func() {
		if len(buf) > 0 {
			h.processLine(string(buf))
		}
		h.done.Break()
	}()

	for chunk := range h.ch {
		buf = append(buf, chunk...)

		for {
			idx := bytes.IndexByte(buf, '\n')
			if idx < 0 {
				break
			}
			line := string(buf[:idx])
			buf = buf[idx+1:]
			h.processLine(line)
		}
	}
}

func (h *HandlerLogger) processLine(line string) {
	if len(line) == 0 {
		return
	}

	if line[len(line)-1] == '}' {
		fmt.Println(line)
		return
	}

	// gstreamer stderr (timestamp-prefixed)
	if strings.HasPrefix(line, "0:00:0") {
		return
	}

	// glib/gobject warnings from gstreamer
	if strings.HasPrefix(line, "(egress:") {
		h.l.Warnw(line, nil)
		return
	}

	// pion SDK stderr output
	if len(line) > 5 && sdkPrefixes[line[:5]] {
		h.l.Infow(line)
		return
	}

	h.l.Errorw(line, nil)
}
