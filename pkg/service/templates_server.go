package service

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"

	"github.com/livekit/protocol/logger"
)

var (
	//go:embed templates
	templatedEmbedFs embed.FS
)

func (s *Service) StartTemplatesServer() error {
	if s.conf.TemplatesPort == 0 {
		logger.Debugw("templates server disabled")
		return nil
	}

	rfs, err := fs.Sub(templatedEmbedFs, "templates")
	if err != nil {
		return err
	}

	h := http.FileServer(http.FS(rfs))

	mux := http.NewServeMux()
	mux.Handle("/", h)

	go func() {
		addr := fmt.Sprintf("localhost:%d", s.conf.TemplatesPort)
		logger.Debugw(fmt.Sprintf("starting template server on address %s", addr))
		_ = http.ListenAndServe(addr, mux)
	}()

	return nil
}
