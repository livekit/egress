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

	"github.com/livekit/protocol/logger"
)

// CmdLogger logs cmd outputs
type CmdLogger struct {
	name string
}

func NewCmdLogger(name string) *CmdLogger {
	return &CmdLogger{
		name: name,
	}
}

func (l *CmdLogger) Write(p []byte) (int, error) {
	logger.Infow(fmt.Sprintf("%s: %s", l.name, string(p)))
	return len(p), nil
}

// HandlerLogger catches stray outputs from egress handlers
type HandlerLogger struct {
	logger logger.Logger
}

func NewHandlerLogger(handlerID, egressID string) *HandlerLogger {
	return &HandlerLogger{
		logger: logger.GetLogger().WithValues("handlerID", handlerID, "egressID", egressID),
	}
}

func (l *HandlerLogger) Write(p []byte) (n int, err error) {
	s := string(p)
	if strings.HasSuffix(s, "}\n") {
		// normal handler logs
		fmt.Print(s)
	} else if strings.HasPrefix(s, "0:00:") {
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
