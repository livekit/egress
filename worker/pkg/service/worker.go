package service

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/livekit/protocol/utils"
	"github.com/pkg/errors"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/livekit-recording/worker/pkg/config"
	"github.com/livekit/livekit-recording/worker/pkg/logger"
	livekit "github.com/livekit/livekit-recording/worker/proto"
)

type Worker struct {
	ctx      context.Context
	rc       *redis.Client
	defaults *config.Config
	kill     chan struct{}
	status   Status
	mock     bool
}

type Status string

const (
	Available Status = "available"
	Reserved  Status = "reserved"
	Recording Status = "recording"
)

func InitializeWorker(conf *config.Config, rc *redis.Client) *Worker {
	return &Worker{
		ctx:      context.Background(),
		rc:       rc,
		defaults: conf,
		kill:     make(chan struct{}),
		status:   Available,
		mock:     conf.Test,
	}
}

func (w *Worker) Start() error {
	logger.Debugw("Starting worker", "mock", w.mock)

	reservations := w.rc.Subscribe(w.ctx, utils.ReservationChannel)
	defer reservations.Close()

	for msg := range reservations.Channel() {
		logger.Debugw("Request received")
		req := &livekit.RecordingReservation{}
		err := proto.Unmarshal([]byte(msg.Payload), req)
		if err != nil {
			return err
		}

		if req.SubmittedAt < time.Now().Add(-utils.ReservationTimeout).UnixNano() {
			logger.Debugw("Discarding old request", "ID", req.Id)
			continue
		}

		key := w.getKey(req.Id)
		claimed, start, stop, err := w.Claim(req.Id, key)
		if err != nil {
			logger.Errorw("Request failed", err, "ID", req.Id)
			return err
		} else if !claimed {
			logger.Debugw("Request locked", "ID", req.Id)
			continue
		}
		logger.Debugw("Request claimed", "ID", req.Id)

		err = w.Run(req, key, start, stop)
		if err != nil {
			logger.Errorw("Recorder failed", err)
			return err
		}
	}

	return nil
}

func (w *Worker) Claim(id, key string) (locked bool, start, stop *redis.PubSub, err error) {
	locked, err = w.rc.SetNX(w.ctx, key, rand.Int(), 0).Result()
	if !locked || err != nil {
		return
	}

	w.status = Reserved
	start = w.rc.Subscribe(w.ctx, utils.StartRecordingChannel(id))
	stop = w.rc.Subscribe(w.ctx, utils.EndRecordingChannel(id))
	err = w.rc.Publish(w.ctx, utils.ReservationResponseChannel(id), nil).Err()
	if err != nil {
		_ = start.Close()
		_ = stop.Close()
		_ = w.rc.Del(w.ctx, key).Err()
	}
	return
}

func (w *Worker) Run(req *livekit.RecordingReservation, key string, start, stop *redis.PubSub) error {
	defer func() {
		_ = start.Close()
		_ = stop.Close()
		if err := w.rc.Del(w.ctx, key).Err(); err != nil {
			logger.Errorw("failed to unlock job", err, "ID", req.Id)
		}
		w.status = Available
	}()

	<-start.Channel()
	w.status = Recording

	conf, err := config.Merge(w.defaults, req)
	if err != nil {
		return errors.Wrap(err, "failed to build recorder config")
	}

	// Launch node recorder
	var cmd *exec.Cmd
	logger.Debugw("Launching recorder", "ID", req.Id)
	if w.mock {
		cmd = exec.Command("sleep", "5")
	} else {
		cmd = exec.Command(
			fmt.Sprintf("LIVEKIT_RECORDING_CONFIG=%s", conf),
			"ts-node", "recorder/src/record.ts",
		)
	}

	err = cmd.Start()
	if err != nil {
		return errors.Wrap(err, "failed to launch recorder")
	}
	logger.Debugw("Recording started", "ID", req.Id)
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err = <-done:
		if err != nil {
			logger.Errorw("Recording failed", err, "ID", req.Id)
		} else {
			logger.Infow("Recording finished", "ID", req.Id)
		}
	case <-stop.Channel():
		logger.Infow("Recording stopped by livekit server", "ID", req.Id)
		if err = cmd.Process.Signal(os.Interrupt); err != nil {
			logger.Errorw("Failed to interrupt recording", err, "ID", req.Id)
		}
	case <-w.kill:
		logger.Infow("Recording stopped by recording service interrupt", "ID", req.Id)
		if err = cmd.Process.Signal(os.Interrupt); err != nil {
			logger.Errorw("Failed to interrupt recording", err, "ID", req.Id)
		}
	}

	return nil
}

func (w *Worker) Stop() {
	w.kill <- struct{}{}
}

func (w *Worker) getKey(id string) string {
	return fmt.Sprintf("recording-lock-%s", id)
}
