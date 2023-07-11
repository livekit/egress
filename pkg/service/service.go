package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/frostbyte73/core"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/version"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/psrpc"
)

const shutdownTimer = time.Second * 30

type Service struct {
	conf        *config.ServiceConfig
	rpcServerV0 egress.RPCServer
	psrpcServer rpc.EgressInternalServer
	ioClient    rpc.IOInfoClient
	promServer  *http.Server
	monitor     *stats.Monitor
	manager     *ProcessManager

	shutdown core.Fuse
}

func NewService(conf *config.ServiceConfig, bus psrpc.MessageBus, rpcServerV0 egress.RPCServer, ioClient rpc.IOInfoClient) (*Service, error) {
	monitor := stats.NewMonitor(conf)

	s := &Service{
		conf:        conf,
		rpcServerV0: rpcServerV0,
		ioClient:    ioClient,
		monitor:     monitor,
		shutdown:    core.NewFuse(),
	}
	s.manager = NewProcessManager(conf, monitor, s.onFatalError)

	psrpcServer, err := rpc.NewEgressInternalServer(conf.NodeID, s, bus)
	if err != nil {
		return nil, err
	}
	s.psrpcServer = psrpcServer

	if conf.PrometheusPort > 0 {
		s.promServer = &http.Server{
			Addr:    fmt.Sprintf(":%d", conf.PrometheusPort),
			Handler: promhttp.Handler(),
		}
	}

	if err = s.monitor.Start(s.conf, s.isAvailable); err != nil {
		return nil, err
	}

	if s.promServer != nil {
		promListener, err := net.Listen("tcp", s.promServer.Addr)
		if err != nil {
			return nil, err
		}
		go func() {
			_ = s.promServer.Serve(promListener)
		}()
	}

	return s, nil
}

func (s *Service) Run() error {
	logger.Debugw("starting service", "version", version.Version)

	if err := s.psrpcServer.RegisterStartEgressTopic(s.conf.ClusterID); err != nil {
		return err
	}

	if s.rpcServerV0 != nil {
		return s.runV0()
	}

	logger.Infow("service ready")
	<-s.shutdown.Watch()
	logger.Infow("shutting down")

	return nil
}

func (s *Service) Reset() {
	if !s.shutdown.IsBroken() {
		s.Stop(false)
	}

	s.shutdown = core.NewFuse()
}

func (s *Service) StartEgress(ctx context.Context, req *rpc.StartEgressRequest) (*livekit.EgressInfo, error) {
	ctx, span := tracer.Start(ctx, "Service.StartEgress")
	defer span.End()

	if err := s.monitor.AcceptRequest(req); err != nil {
		return nil, err
	}

	logger.Infow("request received", "egressID", req.EgressId)

	p, err := config.GetValidatedPipelineConfig(s.conf, req)
	if err != nil {
		s.monitor.EgressAborted(req)
		return nil, err
	}

	requestType, outputType := getTypes(p.Info)
	logger.Infow("request validated",
		"egressID", req.EgressId,
		"requestType", requestType,
		"outputType", outputType,
		"room", p.Info.RoomName,
		"request", p.Info.Request,
	)

	err = s.manager.launchHandler(req, p.Info, 1)
	if err != nil {
		s.monitor.EgressAborted(req)
		return nil, err
	}

	return p.Info, nil
}

func (s *Service) StartEgressAffinity(req *rpc.StartEgressRequest) float32 {
	if !s.monitor.CanAcceptRequest(req) {
		// cannot accept
		return -1
	}

	if s.manager.isIdle() {
		// group multiple track and track composite requests.
		// if this instance is idle and another is already handling some, the request will go to that server.
		// this avoids having many instances with one track request each, taking availability from room composite.
		return 0.5
	} else {
		// already handling a request and has available cpu
		return 1
	}
}

func (s *Service) ListActiveEgress(ctx context.Context, _ *rpc.ListActiveEgressRequest) (*rpc.ListActiveEgressResponse, error) {
	ctx, span := tracer.Start(ctx, "Service.ListActiveEgress")
	defer span.End()

	egressIDs := s.manager.listEgress()
	return &rpc.ListActiveEgressResponse{
		EgressIds: egressIDs,
	}, nil
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

func (s *Service) onFatalError(info *livekit.EgressInfo) {
	sendUpdate(context.Background(), s.ioClient, info)
	s.Stop(false)
}

func (s *Service) Stop(kill bool) {
	s.shutdown.Once(func() {
		s.psrpcServer.DeregisterStartEgressTopic(s.conf.ClusterID)
	})
	if kill {
		s.manager.killAll()
	}
}

func (s *Service) KillAll() {
	s.manager.killAll()
}

func (s *Service) Close() {
	for !s.manager.isIdle() {
		time.Sleep(shutdownTimer)
	}
	logger.Infow("closing server")
	s.psrpcServer.Shutdown()
}
