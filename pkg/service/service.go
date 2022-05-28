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

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/egress/pkg/sysload"
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

			go s.handleRequest(req)
			logger.Debugw("request handled!")
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

func (s *Service) handleRequest(req *livekit.StartEgressRequest) {
	// check request time
	if time.Since(time.Unix(0, req.SentAt)) >= egress.LockDuration {
		return
	}

	// check cpu load
	//var isRoomComposite bool
	switch req.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		//isRoomComposite = true

		// limit to one web composite at a time for now
		if !s.isIdle() {
			logger.Debugw("rejecting web composite request, already recording")
			return
		}
	}

	logger.Infow("EGRESS_REQUEST: ", req)
	if !sysload.CanAcceptRequest(req) {
		logger.Debugw("rejecting request, not enough cpu")
		return
	}

	// claim request
	claimed, err := s.bus.Lock(s.ctx, egress.RequestChannel(req.EgressId), egress.LockDuration)
	if err != nil {
		logger.Errorw("could not claim request", err)
		return
	} else if !claimed {
		return
	}

	sysload.AcceptRequest(req)
	logger.Debugw("request claimed", "egressID", req.EgressId)
	go s.configurePipeline(req)
}

func (s *Service) configurePipeline(req *livekit.StartEgressRequest) {
	// build/verify params
	pipelineParams, err := params.GetPipelineParams(s.conf, req)
	info := pipelineParams.Info
	if err != nil {
		info.Error = err.Error()
		info.Status = livekit.EgressStatus_EGRESS_COMPLETE
		s.sendRPCResponse(req.RequestId, pipelineParams.Info, err)
		return
	}

	s.sendRPCResponse(req.RequestId, pipelineParams.Info, nil)

	// create the pipeline
	p, err := pipeline.New(s.conf, pipelineParams)
	if err != nil {
		info.Error = err.Error()
		info.Status = livekit.EgressStatus_EGRESS_COMPLETE
		s.sendEgressUpdate(info)
		return
	}

	p.OnStatusUpdate(s.sendEgressUpdate)

	s.pipelines.Store(req.EgressId, p.GetInfo())
	s.handleEgress(p)
	/*if isRoomComposite {
		// isolate web composite for now
		s.handleEgress(p)
	} else {
		// track composite and track can run multiple at once
		go s.handleEgress(p)
	}*/
}

// TODO: Run each pipeline in a separate process for security reasons
func (s *Service) handleEgress(p *pipeline.Pipeline) {
	defer s.pipelines.Delete(p.GetInfo().EgressId)

	// subscribe to request channel
	requests, err := s.bus.Subscribe(s.ctx, egress.RequestChannel(p.GetInfo().EgressId))
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
			p.SendEOS()

		case res := <-result:
			// recording finished
			s.sendEgressUpdate(res)
			return

		case msg := <-requests.Channel():
			// request received
			request := &livekit.EgressRequest{}
			err = proto.Unmarshal(requests.Payload(msg), request)
			if err != nil {
				logger.Errorw("failed to read request", err, "egressID", p.GetInfo().EgressId)
				continue
			}
			logger.Debugw("handling request", "egressID", p.GetInfo().EgressId, "requestID", request.RequestId)

			switch req := request.Request.(type) {
			case *livekit.EgressRequest_UpdateStream:
				err = p.UpdateStream(req.UpdateStream)
			case *livekit.EgressRequest_Stop:
				p.SendEOS()
			default:
				err = errors.ErrInvalidRPC
			}

			s.sendRPCResponse(request.RequestId, p.GetInfo(), err)
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

func (s *Service) sendRPCResponse(requestID string, info *livekit.EgressInfo, err error) {
	res := &livekit.EgressResponse{Info: info}
	args := []interface{}{"egressID", info.EgressId, "requestId", requestID}

	if err != nil {
		logger.Errorw("error handling request", err, args...)
		res.Error = err.Error()
	} else {
		logger.Debugw("request handled", args...)
	}

	if err = s.bus.Publish(s.ctx, egress.ResponseChannel(requestID), res); err != nil {
		logger.Errorw("could not send response", err)
	}
}

func (s *Service) sendEgressUpdate(info *livekit.EgressInfo) {
	if info.Error != "" {
		logger.Errorw("egress failed", errors.New(info.Error), "egressID", info.EgressId)
	} else if info.Status == livekit.EgressStatus_EGRESS_COMPLETE {
		logger.Infow("egress completed successfully", "egressID", info.EgressId)
	} else {
		logger.Infow("egress updated", "egressID", info.EgressId, "status", info.Status)
	}

	if err := s.bus.Publish(s.ctx, egress.ResultsChannel, info); err != nil {
		logger.Errorw("failed to send egress update", err)
	}
}
