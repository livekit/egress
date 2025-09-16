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

	"github.com/aws/smithy-go/logging"
	"github.com/linkdata/deadlock"

	"github.com/livekit/protocol/logger"
)

// S3Logger only logs aws messages on upload failure
type S3Logger struct {
	mu   deadlock.Mutex
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
