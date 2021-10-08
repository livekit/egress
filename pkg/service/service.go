package service

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/livekit/protocol/logger"
	livekit "github.com/livekit/protocol/proto"
	"github.com/livekit/protocol/recording"
	"github.com/livekit/protocol/utils"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/livekit-recorder/pkg/config"
	"github.com/livekit/livekit-recorder/pkg/recorder"
)

type Service struct {
	ctx      context.Context
	conf     *config.Config
	bus      utils.MessageBus
	status   atomic.Value // Status
	shutdown chan struct{}
	kill     chan struct{}

	rec         *recorder.Recorder
	recordingId string
}

type Status string

const (
	Available Status = "available"
	Reserved  Status = "reserved"
	Recording Status = "recording"
	Stopping  Status = "stopping"
)

func NewService(conf *config.Config, bus utils.MessageBus) *Service {
	return &Service{
		ctx:      context.Background(),
		conf:     conf,
		bus:      bus,
		status:   atomic.Value{},
		shutdown: make(chan struct{}, 1),
		kill:     make(chan struct{}, 1),
	}
}

func (s *Service) Run() error {
	// TODO: catch panics

	logger.Debugw("Starting service")

	reservations, err := s.bus.SubscribeQueue(context.Background(), recording.ReservationChannel)
	if err != nil {
		return err
	}
	defer reservations.Close()

	for {
		s.status.Store(Available)
		logger.Debugw("Recorder waiting")

		select {
		case <-s.shutdown:
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

			if req.SubmittedAt < time.Now().Add(-recording.ReservationTimeout).UnixNano()/1e6 {
				logger.Debugw("Discarding old request", "ID", req.Id)
				continue
			}

			s.status.Store(Reserved)
			logger.Debugw("Request claimed", "ID", req.Id)

			// handleRecording blocks until recording is finished
			s.recordingId = req.Id
			s.rec = recorder.NewRecorder(s.conf)
			s.handleRecording()
			s.rec = nil
			s.recordingId = ""
		}
	}
}

func (s *Service) Status() Status {
	return s.status.Load().(Status)
}

func (s *Service) Stop(kill bool) {
	s.shutdown <- struct{}{}
	if kill {
		s.kill <- struct{}{}
	}
}
