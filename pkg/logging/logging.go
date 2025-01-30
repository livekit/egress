// Copyright 2025 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package logging

import (
	"fmt"
	"strings"
	"sync"

	"github.com/aws/smithy-go/logging"

	"github.com/livekit/protocol/logger"
)

// DowngradeLogger converts errors to warnings
type DowngradeLogger struct {
	logger.Logger
}

func NewDowngradeLogger() *DowngradeLogger {
	return &DowngradeLogger{
		Logger: logger.GetLogger(),
	}
}

func (l *DowngradeLogger) Errorw(msg string, err error, keysAndValues ...interface{}) {
	l.Logger.Warnw(msg, err, keysAndValues...)
}

// InfoLogger logs command outputs
type InfoLogger struct {
	cmd string
}

func NewInfoLogger(cmd string) *InfoLogger {
	return &InfoLogger{
		cmd: cmd,
	}
}

func (l *InfoLogger) Write(p []byte) (int, error) {
	logger.Infow(fmt.Sprintf("%s: %s", l.cmd, string(p)))
	return len(p), nil
}

// ProcessLogger catches stray outputs from handlers
type ProcessLogger struct {
	logger logger.Logger
}

func NewProcessLogger(handlerID, egressID string) *ProcessLogger {
	return &ProcessLogger{
		logger: logger.GetLogger().WithValues("handlerID", handlerID, "egressID", egressID),
	}
}

func (l *ProcessLogger) Write(p []byte) (n int, err error) {
	s := string(p)
	if strings.HasPrefix(s, "00:00:") {
		// ignore cuda and template not mapped gstreamer warnings
	} else if strings.HasPrefix(s, "turnc") {
		// warn on turnc error
		l.logger.Warnw(s, nil)
	} else {
		// panics and unexpected errors
		l.logger.Errorw(s, nil)
	}
	return len(p), nil
}

// S3Logger only logs aws messages on upload failure
type S3Logger struct {
	mu   sync.Mutex
	msgs []string
	idx  int
}

func NewS3Logger() *S3Logger {
	return &S3Logger{
		msgs: make([]string, 10),
	}
}

func (l *S3Logger) Logf(classification logging.Classification, format string, v ...interface{}) {
	format = "aws %s: " + format
	v = append([]interface{}{strings.ToLower(string(classification))}, v...)

	l.mu.Lock()
	l.msgs[l.idx%len(l.msgs)] = fmt.Sprintf(format, v...)
	l.idx++
	l.mu.Unlock()
}

func (l *S3Logger) WriteLogs() {
	l.mu.Lock()
	size := len(l.msgs)
	for range size {
		if msg := l.msgs[l.idx%size]; msg != "" {
			logger.Debugw(msg)
		}
		l.idx++
	}
	l.mu.Unlock()
}
