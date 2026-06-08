package logging

import (
	"context"
	"fmt"
	"strings"

	lklogger "github.com/livekit/protocol/logger"
)

type RedisLogger struct {
	logger lklogger.Logger
}

func NewRedisLogger(l lklogger.Logger) RedisLogger {
	return RedisLogger{logger: l}
}

func (l RedisLogger) Printf(_ context.Context, format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	if IsRedisSentinelDiscoveryMessage(msg) {
		l.logger.Debugw(msg)
		return
	}

	l.logger.Warnw(msg, nil)
}

func IsRedisSentinelDiscoveryMessage(msg string) bool {
	if !strings.Contains(msg, "sentinel: ") {
		return false
	}

	switch {
	case strings.Contains(msg, "sentinel: selected addr="):
		return true
	case strings.Contains(msg, "sentinel: new master="):
		return true
	case strings.Contains(msg, "sentinel: GetMasterAddrByName ") && strings.HasSuffix(msg, " failed: context canceled"):
		return true
	default:
		return false
	}
}
