package main

import (
	"net/http"

	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/protocol/logger"
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

type handlerDebugHandler struct {
	h *service.Handler
}

func (d *handlerDebugHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	dot, err := d.h.GetPipelineDebugInfo()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")

	w.Write(dot)
}
