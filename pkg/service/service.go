package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/egress/pkg/sysload"
)

const shutdownTimer = time.Second * 30

type Service struct {
	conf      *config.Config
	rpcServer egress.RPCServer

	promServer *http.Server

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

	sysload.Init(s.conf, s.shutdown, func() float64 {
		if s.isIdle() {
			return 1
		}
		return 0
	})

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
			req := &livekit.StartEgressRequest{}
			if err = proto.Unmarshal(requests.Payload(msg), req); err != nil {
				logger.Errorw("malformed request", err)
				continue
			}

			if s.acceptRequest(req) {
				// validate before launching handler
				pipelineParams, err := params.GetPipelineParams(s.conf, req)
				s.sendResponse(req, pipelineParams.Info, err)
				if err != nil {
					continue
				}

				switch req.Request.(type) {
				case *livekit.StartEgressRequest_RoomComposite:
					s.handlingRoomComposite.Store(true)
					go func() {
						s.launchHandler(req)
						s.handlingRoomComposite.Store(false)
					}()
				default:
					go s.launchHandler(req)
				}
			}
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

func (s *Service) acceptRequest(req *livekit.StartEgressRequest) bool {
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

	if !sysload.CanAcceptRequest(req) {
		args = append(args, "reason", "not enough cpu")
		logger.Debugw("rejecting request", args...)
		return false
	}

	// claim request
	claimed, err := s.rpcServer.ClaimRequest(context.Background(), req)
	if err != nil {
		logger.Errorw("could not claim request", err, args...)
		return false
	} else if !claimed {
		return false
	}

	sysload.AcceptRequest(req)
	logger.Infow("request claimed", args...)

	return true
}

func (s *Service) sendResponse(req *livekit.StartEgressRequest, info *livekit.EgressInfo, err error) {
	if err != nil {
		logger.Infow("bad request", err,
			"egressID", info.EgressId,
			"requestID", req.RequestId,
			"senderID", req.SenderId,
		)
	}

	if err = s.rpcServer.SendResponse(context.Background(), req, info, err); err != nil {
		logger.Errorw("failed to send response", err)
	}
}

func (s *Service) launchHandler(req *livekit.StartEgressRequest) {
	confString, err := yaml.Marshal(s.conf)
	if err != nil {
		logger.Errorw("could not marshal config", err)
		return
	}

	reqString, err := proto.Marshal(req)
	if err != nil {
		logger.Errorw("could not marshal request", err)
		return
	}

	cmd := exec.Command("egress",
		"run-handler",
		"--config-body", string(confString),
		"--request", string(reqString),
	)
	cmd.Dir = "/"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	s.processes.Store(req.EgressId, &process{
		req: req,
		cmd: cmd,
	})
	defer s.processes.Delete(req.EgressId)

	err = cmd.Run()
	if err != nil {
		logger.Errorw("could not launch handler", err)
	}
}

func (s *Service) Status() ([]byte, error) {
	info := map[string]interface{}{
		"CpuLoad": sysload.GetCPULoad(),
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
			p := value.(*process)
			if err := p.cmd.Process.Kill(); err != nil {
				logger.Errorw("failed to kill process", err, "egressID", key.(string))
			}
			return true
		})
	}
}
