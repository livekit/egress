package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/livekit/protocol/logger"
	livekit "github.com/livekit/protocol/proto"
	"github.com/livekit/protocol/utils"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/livekit-recorder/service/pkg/config"
)

type Worker struct {
	ctx      context.Context
	bus      utils.MessageBus
	defaults *config.Config
	status   atomic.Value // Status
	shutdown chan struct{}
	kill     chan struct{}

	mock bool
}

type Status string

const (
	Available    Status = "available"
	Reserved     Status = "reserved"
	Recording    Status = "recording"
	lockDuration        = time.Second * 5
)

func InitializeWorker(conf *config.Config, bus utils.MessageBus) *Worker {
	return &Worker{
		ctx:      context.Background(),
		bus:      bus,
		defaults: conf,
		status:   atomic.Value{},
		shutdown: make(chan struct{}, 1),
		kill:     make(chan struct{}, 1),
		mock:     conf.Test,
	}
}

func (w *Worker) Start() error {
	logger.Debugw("Starting worker", "mock", w.mock)

	reservations, err := w.bus.Subscribe(context.Background(), utils.ReservationChannel)
	if err != nil {
		return err
	}
	defer reservations.Close()

	for {
		w.status.Store(Available)
		logger.Debugw("Recorder waiting")

		select {
		case <-w.shutdown:
			logger.Debugw("Shutting down")
			return nil
		case msg := <-reservations.Channel():
			logger.Debugw("Request received")

			req := &livekit.RecordingReservation{}
			err := proto.Unmarshal(reservations.Payload(msg), req)
			if err != nil {
				logger.Errorw("Malformed request", err)
				continue
			}

			if req.SubmittedAt < time.Now().Add(-utils.ReservationTimeout).UnixNano() {
				logger.Debugw("Discarding old request", "ID", req.Id)
				continue
			}

			claimed, err := w.bus.Lock(w.ctx, w.getKey(req.Id), lockDuration)
			if err != nil {
				logger.Errorw("Request failed", err, "ID", req.Id)
				return err
			} else if !claimed {
				logger.Debugw("Request already claimed", "ID", req.Id)
				continue
			}

			w.status.Store(Reserved)
			logger.Debugw("Request claimed", "ID", req.Id)

			res, err := w.Record(req)
			b, _ := proto.Marshal(res)
			_ = w.bus.Publish(w.ctx, utils.RecordingResultChannel, b)
			if err != nil {
				return err
			}
		}
	}
}

func (w *Worker) Record(req *livekit.RecordingReservation) (res *livekit.RecordingResult, err error) {
	res = &livekit.RecordingResult{Id: req.Id}
	var startedAt time.Time
	defer func() {
		if err != nil {
			logger.Errorw("Recorder failed", err)
			res.Error = err.Error()
		} else {
			res.Duration = time.Since(startedAt).Milliseconds()
		}
	}()

	start, err := w.bus.Subscribe(w.ctx, utils.StartRecordingChannel(req.Id))
	if err != nil {
		return
	}
	defer start.Close()

	stop, err := w.bus.Subscribe(w.ctx, utils.EndRecordingChannel(req.Id))
	if err != nil {
		return
	}
	defer stop.Close()

	err = w.bus.Publish(w.ctx, utils.ReservationResponseChannel(req.Id), nil)
	if err != nil {
		return
	}

	// send recording started message
	<-start.Channel()
	w.status.Store(Recording)

	conf, err := config.Merge(w.defaults, req)
	if err != nil {
		return
	}

	// Launch node recorder
	var cmd *exec.Cmd
	logger.Debugw("Launching recorder", "ID", req.Id)
	if w.mock {
		cmd = exec.Command("sleep", "3")
	} else {
		cmd = exec.Command("node", "app/src/record.js")
		cmd.Env = append(cmd.Env, fmt.Sprintf("LIVEKIT_RECORDER_CONFIG=%s", conf))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	err = cmd.Start()
	if err != nil {
		return
	}
	startedAt = time.Now()
	logger.Infow("Recording started", "ID", req.Id)

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err = <-done:
		break
	case <-stop.Channel():
		logger.Infow("Recording stopped by livekit server", "ID", req.Id)
		err = cmd.Process.Signal(syscall.SIGTERM)
	case <-w.kill:
		logger.Infow("Recording stopped by recording service interrupt", "ID", req.Id)
		err = cmd.Process.Signal(syscall.SIGTERM)
	}

	return
}

func (w *Worker) Status() Status {
	return w.status.Load().(Status)
}

func (w *Worker) Stop(kill bool) {
	w.shutdown <- struct{}{}
	if kill {
		w.kill <- struct{}{}
	}
}

func (w *Worker) getKey(id string) string {
	return fmt.Sprintf("recording-lock-%s", id)
}
