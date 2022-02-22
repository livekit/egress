package service

import (
	"context"
	"time"

	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
)

type Service struct {
	ctx      context.Context
	conf     *config.Config
	bus      utils.MessageBus
	shutdown chan struct{}
	kill     chan struct{}
}

func NewService(conf *config.Config, bus utils.MessageBus) *Service {
	s := &Service{
		ctx:      context.Background(),
		conf:     conf,
		bus:      bus,
		shutdown: make(chan struct{}),
		kill:     make(chan struct{}),
	}
	return s
}

func (s *Service) Run() error {
	logger.Debugw("starting service")

	requests, err := s.bus.Subscribe(s.ctx, egress.StartChannel)
	if err != nil {
		return err
	}
	defer func() {
		err := requests.Close()
		if err != nil {
			logger.Errorw("failed to unsubscribe", err)
		}
	}()

	for {
		logger.Debugw("waiting for requests")

		select {
		case <-s.shutdown:
			logger.Infow("shutting down")
			return nil
		case msg := <-requests.Channel():
			logger.Debugw("request received")

			req := &livekit.StartEgressRequest{}
			if err = proto.Unmarshal(requests.Payload(msg), req); err != nil {
				logger.Errorw("malformed request", err)
				continue
			}

			// check request time
			if time.Since(time.Unix(0, req.SentAt)) >= egress.LockDuration {
				continue
			}

			// TODO: CPU estimation

			// claim request
			claimed, err := s.bus.Lock(s.ctx, egress.RequestChannel(req.EgressId), egress.LockDuration)
			if err != nil {
				logger.Errorw("could not claim request", err)
				continue
			} else if !claimed {
				continue
			}
			logger.Debugw("request claimed", "egressID", req.EgressId)

			// build/verify params
			pipelineParams, err := params.GetPipelineParams(s.conf, req)
			if err != nil {
				s.sendEgressResponse(req.EgressId, nil, err)
				continue
			} else {
				s.sendEgressResponse(req.EgressId, pipelineParams.Info, nil)

				// TODO: don't block while running
				s.handleEgress(pipelineParams)
			}
		}
	}
}

func (s *Service) Status() string {
	return "TODO"
}

func (s *Service) Stop(kill bool) {
	select {
	case <-s.shutdown:
	default:
		close(s.shutdown)
	}

	if kill {
		select {
		case <-s.kill:
		default:
			close(s.kill)
		}
	}
}

func (s *Service) handleEgress(pipelineParams *params.Params) {
	// create the pipeline
	p, err := pipeline.FromParams(s.conf, pipelineParams)
	if err != nil {
		info := pipelineParams.Info
		info.Error = err.Error()
		s.sendEgressResult(info)
		return
	}

	// subscribe to request channel
	requests, err := s.bus.Subscribe(s.ctx, egress.RequestChannel(p.Info().EgressId))
	if err != nil {
		return
	}
	defer func() {
		err := requests.Close()
		if err != nil {
			logger.Errorw("failed to unsubscribe from request channel", err)
		}
	}()

	// start egress
	result := make(chan *livekit.EgressInfo, 1)
	go func() {
		result <- p.Run()
	}()

	for {
		select {
		case <-s.kill:
			// kill signal received
			p.Stop()
		case res := <-result:
			// recording finished
			s.sendEgressResult(res)
			return
		case msg := <-requests.Channel():
			// request received
			request := &livekit.EgressRequest{}
			err = proto.Unmarshal(requests.Payload(msg), request)
			if err != nil {
				logger.Errorw("failed to read request", err, "egressID", p.Info().EgressId)
				continue
			}
			logger.Debugw("handling request", "egressID", p.Info().EgressId, "requestID", request.RequestId)

			switch req := request.Request.(type) {
			case *livekit.EgressRequest_UpdateStream:
				err = p.UpdateStream(req.UpdateStream)
			case *livekit.EgressRequest_Stop:
				p.Stop()
			default:
				err = errors.ErrInvalidRPC
			}

			s.sendEgressResponse(request.RequestId, p.Info(), err)
		}
	}
}

func (s *Service) sendEgressResponse(requestID string, info *livekit.EgressInfo, err error) {
	res := &livekit.EgressResponse{}
	if err != nil {
		logger.Errorw("error handling request", err,
			"egressID", info.EgressId, "requestId", requestID)
		res.Error = err.Error()
	} else {
		logger.Debugw("request handled", "egressID", info.EgressId, "requestId", requestID)
		res.Info = info
	}

	if err = s.bus.Publish(s.ctx, egress.ResponseChannel(requestID), res); err != nil {
		logger.Errorw("could not send response", err)
	}
}

func (s *Service) sendEgressResult(res *livekit.EgressInfo) {
	if res.Error != "" {
		logger.Errorw("recording failed", errors.New(res.Error), "egressID", res.EgressId)
	} else {
		logger.Infow("recording complete", "egressID", res.EgressId)
	}

	if err := s.bus.Publish(s.ctx, egress.ResultsChannel, res); err != nil {
		logger.Errorw("failed to write results", err)
	}
}
