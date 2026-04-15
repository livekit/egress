package logging

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"go.uber.org/atomic"

	"github.com/livekit/protocol/logger"
)

const (
	channelSize     = 4096
	dropLogThrottle = 10 * time.Second
)

type HandlerLogger struct {
	ch          chan []byte
	done        chan struct{}
	dropped     atomic.Int64
	lastDropLog atomic.Int64 // unix nanos
	l           logger.Logger
}

func NewHandlerLogger(handlerID, egressID string) *HandlerLogger {
	h := &HandlerLogger{
		ch:   make(chan []byte, channelSize),
		done: make(chan struct{}),
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
	<-h.done
	return nil
}

func (h *HandlerLogger) drain() {
	var buf []byte
	var panicBuf []string

	defer func() {
		// flush remaining buffer
		if len(buf) > 0 {
			h.processLine(string(buf), &panicBuf)
		}
		// flush any accumulated panic
		if len(panicBuf) > 0 {
			h.l.Errorw(strings.Join(panicBuf, "\n"), nil)
		}
		close(h.done)
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
			h.processLine(line, &panicBuf)
		}
	}
}

func (h *HandlerLogger) processLine(line string, panicBuf *[]string) {
	if len(line) == 0 {
		return
	}

	if line[len(line)-1] == '}' {
		if len(*panicBuf) > 0 {
			h.flushPanic(panicBuf)
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
		*panicBuf = append(*panicBuf, line)
		return
	}

	// panic accumulation
	if len(*panicBuf) > 0 {
		if h.isPanicContinuation(line) {
			*panicBuf = append(*panicBuf, line)
			return
		}
		h.flushPanic(panicBuf)
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

func (h *HandlerLogger) flushPanic(panicBuf *[]string) {
	if len(*panicBuf) > 0 {
		h.l.Errorw(strings.Join(*panicBuf, "\n"), nil)
		*panicBuf = nil
	}
}
