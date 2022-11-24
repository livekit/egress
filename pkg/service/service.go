package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/version"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

const shutdownTimer = time.Second * 30

type Service struct {
	conf       *config.ServiceConfig
	rpcServer  egress.RPCServer
	promServer *http.Server
	monitor    *stats.Monitor
	manager    *Manager

	shutdown chan struct{}
}

func NewService(conf *config.ServiceConfig, rpcServer egress.RPCServer) *Service {
	monitor := stats.NewMonitor()

	s := &Service{
		conf:      conf,
		rpcServer: rpcServer,
		monitor:   monitor,
		manager:   NewManager(conf, monitor),
		shutdown:  make(chan struct{}),
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
	logger.Debugw("starting service", "version", version.Version)

	if s.promServer != nil {
		promListener, err := net.Listen("tcp", s.promServer.Addr)
		if err != nil {
			return err
		}
		go func() {
			_ = s.promServer.Serve(promListener)
		}()
	}

	if err := s.monitor.Start(s.conf, s.isAvailable); err != nil {
		return err
	}

	requests, err := s.rpcServer.GetRequestChannel(context.Background())
	if err != nil {
		return err
	}

	defer func() {
		_ = requests.Close()
	}()

	logger.Debugw("service ready")

	for {
		select {
		case <-s.shutdown:
			logger.Infow("shutting down")
			for !s.manager.isIdle() {
				time.Sleep(shutdownTimer)
			}
			return nil

		case msg := <-requests.Channel():
			req := &livekit.StartEgressRequest{}
			if err = proto.Unmarshal(requests.Payload(msg), req); err != nil {
				logger.Errorw("malformed request", err)
				continue
			}

			s.handleRequest(req)
		}
	}
}

func (s *Service) handleRequest(req *livekit.StartEgressRequest) {
	ctx, span := tracer.Start(context.Background(), "Service.handleRequest")
	defer span.End()

	if s.acceptRequest(ctx, req) {
		// validate before passing to handler
		info, err := config.ValidateRequest(ctx, s.conf, req)
		if err == nil {
			err = s.manager.launchHandler(req)
		}

		s.sendResponse(ctx, req, info, err)
		if err != nil {
			span.RecordError(err)
		}
	}
}

func (s *Service) acceptRequest(ctx context.Context, req *livekit.StartEgressRequest) bool {
	ctx, span := tracer.Start(ctx, "Service.acceptRequest")
	defer span.End()

	args := []interface{}{
		"egressID", req.EgressId,
		"requestID", req.RequestId,
		"senderID", req.SenderId,
	}
	logger.Debugw("request received", args...)

	// check request time
	if time.Since(time.Unix(0, req.SentAt)) >= egress.RequestExpiration {
		return false
	}

	// check if already handling web
	if !s.manager.canAccept(req) {
		args = append(args, "reason", "only one web egress allowed")
		logger.Debugw("rejecting request", args...)
		return false
	}

	// check cpu load
	if !s.monitor.CanAcceptRequest(req) {
		args = append(args, "reason", "not enough cpu")
		logger.Debugw("rejecting request", args...)
		return false
	}

	// claim request
	claimed, err := s.rpcServer.ClaimRequest(context.Background(), req)
	if err != nil {
		logger.Warnw("could not claim request", err, args...)
		return false
	} else if !claimed {
		return false
	}

	// accept request
	s.monitor.AcceptRequest(req)
	logger.Infow("request accepted", args...)
	return true
}

func (s *Service) sendResponse(ctx context.Context, req *livekit.StartEgressRequest, info *livekit.EgressInfo, err error) {
	if err != nil {
		logger.Infow("bad request",
			"error", err,
			"egressID", info.EgressId,
			"requestID", req.RequestId,
			"senderID", req.SenderId,
		)
	}

	if err = s.rpcServer.SendResponse(ctx, req, info, err); err != nil {
		logger.Errorw("failed to send response", err)
	}
}

func (s *Service) Status() ([]byte, error) {
	return json.Marshal(s.manager.status())
}

func (s *Service) isAvailable() float64 {
	if s.manager.isIdle() {
		return 1
	}
	return 0
}

func (s *Service) Stop(kill bool) {
	select {
	case <-s.shutdown:
	default:
		close(s.shutdown)
	}

	if kill {
		s.manager.shutdown()
	}
}
