package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
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
	ctx  context.Context
	conf *config.Config
	bus  utils.MessageBus

	promServer *http.Server

	pipelines sync.Map
	shutdown  chan struct{}
	kill      chan struct{}
}

func NewService(conf *config.Config, bus utils.MessageBus) *Service {
	s := &Service{
		ctx:      context.Background(),
		conf:     conf,
		bus:      bus,
		shutdown: make(chan struct{}),
		kill:     make(chan struct{}),
	}

	if conf.PrometheusPort > 0 {
		s.promServer = &http.Server{
			Addr:    fmt.Sprintf(":%d", conf.PrometheusPort),
			Handler: promhttp.Handler(),
		}
	}

	return s
}

func (s *Service) Run() error {
	logger.Debugw("starting service")

	if s.promServer != nil {
		promListener, err := net.Listen("tcp", s.promServer.Addr)
		if err != nil {
			return err
		}
		go func() {
			_ = s.promServer.Serve(promListener)
		}()
	}

	sysload.Init(s.conf.NodeID, s.shutdown, func() float64 {
		if s.isIdle() {
			return 1
		}
		return 0
	})

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
			for !s.isIdle() {
				time.Sleep(shutdownTimer)
			}
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

			// check cpu load
			var isRoomComposite bool
			switch req.Request.(type) {
			case *livekit.StartEgressRequest_RoomComposite:
				// limit to one web composite at a time for now
				if !s.isIdle() {
					logger.Debugw("rejecting web composite request, already recording")
					continue
				}
				if !sysload.CanAcceptRequest(req) {
					logger.Debugw("rejecting request, not enough cpu")
					continue
				}
				isRoomComposite = true
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
			logger.Debugw("request claimed", "egressID", req.EgressId)

			// build/verify params
			pipelineParams, err := params.GetPipelineParams(s.conf, req)
			if err != nil {
				s.sendEgressResponse(req.RequestId, &livekit.EgressInfo{EgressId: req.EgressId}, err)
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
				if isRoomComposite {
					// isolate web composite for now
					s.handleEgress(p)
				} else {
					go s.handleEgress(p)
				}
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

func (s *Service) isIdle() bool {
	idle := true
	s.pipelines.Range(func(key, value interface{}) bool {
		idle = false
		return false
	})
	return idle
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
