package main

import (
	"net/http"

	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/pkg/service"
)

type httpHandler struct {
	svc *service.Service
}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	info, err := h.svc.Status()
	if err != nil {
		logger.Errorw("failed to read status", err)
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(info)
}
