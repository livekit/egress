package main

import (
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/livekit/protocol/logger"
	"github.com/urfave/cli/v2"

	"github.com/livekit/livekit-recorder/pkg/recorder"
	"github.com/livekit/livekit-recorder/pkg/service"
)

func runRecorder(c *cli.Context) error {
	conf, err := getConfig(c)
	if err != nil {
		return err
	}
	req, err := getRequest(c)
	if err != nil {
		return err
	}

	initLogger(conf.LogLevel)

	rec := recorder.NewRecorder(conf)
	if err = rec.Validate(req); err != nil {
		return err
	}

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		sig := <-stopChan
		logger.Infow("Exit requested, stopping recording and shutting down", "signal", sig)
		rec.Stop()
	}()

	res := rec.Run("standalone")
	service.LogResult(res)
	if res.Error == "" {
		return nil
	}
	return errors.New(res.Error)
}
