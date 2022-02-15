package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline"
)

func runRecorder(conf *config.Config, req *livekit.StartEgressRequest) error {
	req.EgressId = utils.NewGuid(utils.EgressPrefix)
	rec, err := pipeline.FromRequest(conf, req)
	if err != nil {
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
	if res.Error != "" {
		err = errors.New(res.Error)
		logger.Errorw("recording failed", err, "egressID", res.EgressId)
		return err
	}

	logger.Infow("recording complete", "egressID", res.EgressId)
	return nil
}
