package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/version"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

const shutdownTimer = time.Second * 30

type Service struct {
	conf       *config.Config
	rpcServer  egress.RPCServer
	promServer *http.Server
	monitor    *stats.Monitor

	handlingRoomComposite atomic.Bool
	processes             sync.Map
	shutdown              chan struct{}
}

type process struct {
	req *livekit.StartEgressRequest
	cmd *exec.Cmd
}

func NewService(conf *config.Config, rpcServer egress.RPCServer) *Service {
	s := &Service{
		conf:      conf,
		rpcServer: rpcServer,
		monitor:   stats.NewMonitor(),
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

	if err := s.monitor.Start(s.conf, s.shutdown, s.isAvailable); err != nil {
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
			for !s.isIdle() {
				time.Sleep(shutdownTimer)
			}
			return nil

		case msg := <-requests.Channel():
			ctx, span := tracer.Start(context.Background(), "Service.HandleRequest")

			req := &livekit.StartEgressRequest{}
			if err = proto.Unmarshal(requests.Payload(msg), req); err != nil {
				logger.Errorw("malformed request", err)
				span.End()
				continue
			}

			if s.acceptRequest(ctx, req) {
				// validate before launching handler
				info, err := params.ValidateRequest(ctx, s.conf, req)
				s.sendResponse(ctx, req, info, err)
				if err != nil {
					span.RecordError(err)
					span.End()
					continue
				}

				switch req.Request.(type) {
				case *livekit.StartEgressRequest_RoomComposite:
					s.handlingRoomComposite.Store(true)
					go func() {
						s.launchHandler(ctx, req)
						s.handlingRoomComposite.Store(false)
					}()
				default:
					go s.launchHandler(ctx, req)
				}
			}

			span.End()
		}
	}
}

func (s *Service) isIdle() bool {
	idle := true
	s.processes.Range(func(key, value interface{}) bool {
		idle = false
		return false
	})
	return idle
}

func (s *Service) isAvailable() float64 {
	if s.isIdle() {
		return 1
	}
	return 0
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

	if s.handlingRoomComposite.Load() {
		args = append(args, "reason", "already handling room composite")
		logger.Debugw("rejecting request", args...)
		return false
	}

	// check cpu load
	switch req.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		// limit to one web composite at a time for now
		if !s.isIdle() {
			args = append(args, "reason", "already recording")
			logger.Debugw("rejecting request", args...)
			return false
		}
	default:
		// continue
	}

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

func (s *Service) launchHandler(ctx context.Context, req *livekit.StartEgressRequest) {
	ctx, span := tracer.Start(ctx, "Service.launchHandler")
	defer span.End()

	confString, err := yaml.Marshal(s.conf)
	if err != nil {
		span.RecordError(err)
		logger.Errorw("could not marshal config", err)
		return
	}

	reqString, err := protojson.Marshal(req)
	if err != nil {
		span.RecordError(err)
		logger.Errorw("could not marshal request", err)
		return
	}

	tempPath := path.Join(os.TempDir(), req.EgressId)

	cmd := exec.Command("egress",
		"run-handler",
		"--config-body", string(confString),
		"--request", string(reqString),
		"--temp-path", tempPath,
	)
	cmd.Dir = "/"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	s.monitor.EgressStarted(req)
	s.processes.Store(req.EgressId, &process{
		req: req,
		cmd: cmd,
	})

	defer func() {
		s.monitor.EgressEnded(req)
		s.processes.Delete(req.EgressId)
		logger.Debugw("deleting handler temporary directory", "path", tempPath)
		_ = os.RemoveAll(tempPath)
	}()

	if err = cmd.Run(); err != nil {
		logger.Errorw("could not launch handler", err)
	}
}

func (s *Service) Status() ([]byte, error) {
	info := map[string]interface{}{
		"CpuLoad": s.monitor.GetCPULoad(),
	}
	s.processes.Range(func(key, value interface{}) bool {
		p := value.(*process)
		info[key.(string)] = p.req.Request
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
		s.processes.Range(func(key, value interface{}) bool {
			if err := value.(*process).cmd.Process.Signal(syscall.SIGINT); err != nil {
				logger.Errorw("failed to kill process", err, "egressID", key.(string))
			}
			return true
		})
	}
}

func (s *Service) ListEgress() []string {
	res := make([]string, 0)

	s.processes.Range(func(key, value interface{}) bool {
		res = append(res, key.(string))
		return true
	})

	return res
}
