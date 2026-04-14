package logging

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/linkdata/deadlock"

	"github.com/livekit/protocol/logger"
)

type HandlerLogger struct {
	mu       deadlock.Mutex
	buf      []byte
	panicBuf []string
	l        logger.Logger
}

func NewHandlerLogger(handlerID, egressID string) *HandlerLogger {
	return &HandlerLogger{
		l: logger.GetLogger().WithValues(
			"handlerID", handlerID,
			"egressID", egressID,
		),
	}
}

func (h *HandlerLogger) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.buf = append(h.buf, p...)

	for {
		idx := bytes.IndexByte(h.buf, '\n')
		if idx < 0 {
			break
		}
		line := string(h.buf[:idx])
		h.buf = h.buf[idx+1:]
		if len(line) > 0 {
			h.processLine(line)
		}
	}

	return len(p), nil
}

func (h *HandlerLogger) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// flush buffer
	if len(h.buf) > 0 {
		line := string(h.buf)
		h.buf = nil
		if len(line) > 0 {
			h.processLine(line)
		}
	}

	// flush panic
	h.flushPanic()
	return nil
}

func (h *HandlerLogger) processLine(line string) {
	if line[len(line)-1] == '}' {
		if len(h.panicBuf) > 0 {
			h.flushPanic()
		}
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

	// panic entry
	if strings.HasPrefix(line, "panic:") ||
		strings.HasPrefix(line, "fatal error:") ||
		strings.HasPrefix(line, "goroutine ") {
		h.panicBuf = append(h.panicBuf, line)
		return
	}

	// panic accumulation
	if len(h.panicBuf) > 0 {
		if h.isPanicContinuation(line) {
			h.panicBuf = append(h.panicBuf, line)
			return
		}
		h.flushPanic()
	}

	h.l.Errorw(line, nil)
}

func (h *HandlerLogger) isPanicContinuation(line string) bool {
	if line[0] == '\t' {
		return true
	}
	if strings.HasPrefix(line, "goroutine ") {
		return true
	}
	if !strings.HasPrefix(line, "(") && strings.Contains(line, "(") {
		return true
	}
	return false
}

func (h *HandlerLogger) flushPanic() {
	if len(h.panicBuf) > 0 {
		h.l.Errorw(strings.Join(h.panicBuf, "\n"), nil)
		h.panicBuf = nil
	}
}
