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
	"github.com/livekit/protocol/utils"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/sysload"
)

const shutdownTimer = time.Second * 30

type Service struct {
	ctx  context.Context
	conf *config.Config
	bus  utils.MessageBus

	promServer *http.Server

	handlingRoomComposite atomic.Bool
	pipelines             sync.Map
	shutdown              chan struct{}
	kill                  chan struct{}
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
		_ = requests.Close()
		_ = s.bus.Close()
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
			logger.Debugw("request received")

			req := &livekit.StartEgressRequest{}
			if err = proto.Unmarshal(requests.Payload(msg), req); err != nil {
				logger.Errorw("malformed request", err)
				continue
			}

			if s.acceptRequest(req) {
				go s.launchHandler(req)
			}
		}
	}
}

func (s *Service) acceptRequest(req *livekit.StartEgressRequest) bool {
	// check request time
	if time.Since(time.Unix(0, req.SentAt)) >= egress.LockDuration {
		return false
	}

	if s.handlingRoomComposite.Load() {
		logger.Debugw("rejecting request", "reason", "already handling room composite")
		return false
	}

	// check cpu load
	var isRoomComposite bool
	switch req.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		isRoomComposite = true

		// limit to one web composite at a time for now
		if !s.isIdle() {
			logger.Debugw("rejecting request", "reason", "already recording")
			return false
		}
	}

	if !sysload.CanAcceptRequest(req) {
		logger.Debugw("rejecting request", "reason", "not enough cpu")
		return false
	}

	// claim request
	claimed, err := s.bus.Lock(s.ctx, egress.RequestChannel(req.EgressId), egress.LockDuration)
	if err != nil {
		logger.Errorw("could not claim request", err)
		return false
	} else if !claimed {
		return false
	}

	sysload.AcceptRequest(req)
	logger.Debugw("request claimed", "egressID", req.EgressId)

	if isRoomComposite {
		s.handlingRoomComposite.Store(true)
	}

	return true
}

func (s *Service) isIdle() bool {
	idle := true
	s.pipelines.Range(func(key, value interface{}) bool {
		idle = false
		return false
	})
	return idle
}

func (s *Service) launchHandler(req *livekit.StartEgressRequest) {
	s.pipelines.Store(req.EgressId, req)
	defer func() {
		s.pipelines.Delete(req.EgressId)
		s.handlingRoomComposite.CAS(true, false)
	}()

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

	err = cmd.Run()
	if err != nil {
		logger.Errorw("could not launch handler", err)
	}
}

func (s *Service) Status() ([]byte, error) {
	info := map[string]interface{}{
		"CpuLoad": sysload.GetCPULoad(),
	}
	s.pipelines.Range(func(key, value interface{}) bool {
		egressInfo := value.(*livekit.StartEgressRequest)
		info[key.(string)] = egressInfo.Request
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
			// TODO: forward signal to all handlers
		}
	}
}
