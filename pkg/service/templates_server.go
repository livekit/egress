// Copyright 2023 LiveKit, Inc.
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

package service

import (
	"fmt"
	"io/fs"
	"net/http"

	"github.com/livekit/protocol/logger"
)

func (s *Service) StartTemplatesServer(fs fs.FS) error {
	if s.conf.TemplatePort == 0 {
		logger.Debugw("templates server disabled")
		return nil
	}

	h := http.FileServer(http.FS(fs))

	mux := http.NewServeMux()
	mux.Handle("/", h)

	go func() {
		addr := fmt.Sprintf("localhost:%d", s.conf.TemplatePort)
		logger.Debugw(fmt.Sprintf("starting template server on address %s", addr))
		_ = http.ListenAndServe(addr, mux)
	}()

	return nil
}
