package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/messaging"
	"github.com/livekit/egress/pkg/service"
)

func runService(conf *config.Config) error {
	rc, err := messaging.NewMessageBus(conf)
	if err != nil {
		return err
	}
	svc := service.NewService(conf, rc)

	if conf.HealthPort != 0 {
		go func() {
			_ = http.ListenAndServe(fmt.Sprintf(":%d", conf.HealthPort), &handler{svc: svc})
		}()
	}

	finishChan := make(chan os.Signal, 1)
	signal.Notify(finishChan, syscall.SIGTERM, syscall.SIGQUIT)

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGINT)

	go func() {
		select {
		case sig := <-finishChan:
			logger.Infow("exit requested, finishing recording then shutting down", "signal", sig)
			svc.Stop(false)
		case sig := <-stopChan:
			logger.Infow("exit requested, stopping recording and shutting down", "signal", sig)
			svc.Stop(true)
		}
	}()

	return svc.Run()
}

type handler struct {
	svc *service.Service
}

func (h *handler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	info, err := h.svc.Status()
	if err != nil {
		logger.Errorw("failed to read status", err)
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(info)
}
