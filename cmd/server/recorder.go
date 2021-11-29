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

	rec := recorder.NewRecorder(conf, "standalone")
	if err = rec.Validate(req); err != nil {
		return err
	}

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		sig := <-stopChan
		logger.Infow("exit requested, stopping recording and shutting down", "signal", sig)
		rec.Stop()
	}()

	res := rec.Run()
	service.LogResult(res)
	if res.Error == "" {
		return nil
	}
	return errors.New(res.Error)
}
