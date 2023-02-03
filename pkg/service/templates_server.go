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
