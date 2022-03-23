package service

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/livekit-egress/pkg/sysload"
)

const shutdownTimer = time.Second * 30

type Service struct {
	ctx      context.Context
	conf     *config.Config
	bus      utils.MessageBus
	shutdown chan struct{}
	kill     chan struct{}

	pipelines       sync.Map
	hasWebComposite bool
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

	go sysload.MonitorCPULoad(s.shutdown)

	for {
		logger.Debugw("waiting for requests")

		select {
		case <-s.shutdown:
			logger.Infow("shutting down")
			for {
				empty := true
				s.pipelines.Range(func(key, value interface{}) bool {
					empty = false
					return false
				})
				if empty {
					return nil
				}
				time.Sleep(shutdownTimer)
			}
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

			// check cpu load
			var isWebComposite bool
			switch req.Request.(type) {
			case *livekit.StartEgressRequest_WebComposite:
				// limit to one web composite at a time for now
				if s.hasWebComposite || !sysload.CanAcceptRequest(req) {
					logger.Debugw("rejecting request, not enough cpu")
					continue
				}
				isWebComposite = true
			default:
				if !sysload.CanAcceptRequest(req) {
					logger.Debugw("rejecting request, not enough cpu")
					continue
				}
			}

			// claim request
			claimed, err := s.bus.Lock(s.ctx, egress.RequestChannel(req.EgressId), egress.LockDuration)
			if err != nil {
				logger.Errorw("could not claim request", err)
				continue
			} else if !claimed {
				continue
			}

			sysload.AcceptRequest(req)
			if isWebComposite {
				s.hasWebComposite = true
			}

			logger.Debugw("request claimed", "egressID", req.EgressId)

			// build/verify params
			pipelineParams, err := params.GetPipelineParams(s.conf, req)
			if err != nil {
				s.sendEgressResponse(req.RequestId, nil, err)
				continue
			}

			s.sendEgressResponse(req.RequestId, pipelineParams.Info, nil)

			// create the pipeline
			p, err := pipeline.FromParams(s.conf, pipelineParams)
			info := pipelineParams.Info
			if err != nil {
				info.Error = err.Error()
				s.sendEgressResult(info)
			} else {
				s.pipelines.Store(req.EgressId, info)
				go s.handleEgress(p)
			}
		}
	}
}

func (s *Service) Status() ([]byte, error) {
	info := map[string]interface{}{
		"CpuLoad": sysload.GetCPULoad(),
	}
	s.pipelines.Range(func(key, value interface{}) bool {
		egressInfo := value.(*livekit.EgressInfo)
		info[key.(string)] = egressInfo
		return true
	})

	return json.Marshal(info)
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

func (s *Service) handleEgress(p pipeline.Pipeline) {
	defer s.pipelines.Delete(p.Info().EgressId)

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
