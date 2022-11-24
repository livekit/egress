package service

import (
	"context"
	"os"
	"os/exec"
	"path"
	"sync"
	"syscall"

	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/protocol/utils"
)

type Manager struct {
	conf    *config.ServiceConfig
	monitor *stats.Monitor

	mu             sync.RWMutex
	handlingWeb    bool
	activeHandlers map[string]*handler
}

type handler struct {
	handlerID string
	req       *livekit.StartEgressRequest
	cmd       *exec.Cmd
	closed    chan struct{}
}

func NewManager(conf *config.ServiceConfig, monitor *stats.Monitor) *Manager {
	return &Manager{
		conf:           conf,
		monitor:        monitor,
		activeHandlers: make(map[string]*handler),
	}
}

func (s *Manager) canAccept(req *livekit.StartEgressRequest) bool {
	return !s.handlingWeb && (!isWeb(req) || s.isIdle())
}

func (s *Manager) launchHandler(req *livekit.StartEgressRequest) error {
	_, span := tracer.Start(context.Background(), "Service.launchHandler")
	defer span.End()

	handlerID := utils.NewGuid("EGH_")
	p := &config.PipelineConfig{
		BaseConfig: s.conf.BaseConfig,
		HandlerID:  handlerID,
		TmpDir:     path.Join(os.TempDir(), handlerID),
	}

	confString, err := yaml.Marshal(p)
	if err != nil {
		span.RecordError(err)
		logger.Errorw("could not marshal config", err)
		return err
	}

	reqString, err := protojson.Marshal(req)
	if err != nil {
		span.RecordError(err)
		logger.Errorw("could not marshal request", err)
		return err
	}

	cmd := exec.Command("egress",
		"run-handler",
		"--config", string(confString),
		"--request", string(reqString),
	)
	cmd.Dir = "/"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err = cmd.Start(); err != nil {
		span.RecordError(err)
		logger.Errorw("could not launch handler", err)
		return err
	}

	s.monitor.EgressStarted(req)
	h := &handler{
		handlerID: handlerID,
		req:       req,
		cmd:       cmd,
		closed:    make(chan struct{}),
	}

	s.mu.Lock()
	s.activeHandlers[req.EgressId] = h
	s.mu.Unlock()

	go s.awaitCleanup(h)

	return nil
}

func (s *Manager) awaitCleanup(h *handler) {
	if err := h.cmd.Wait(); err != nil {
		logger.Errorw("handler failed", err)
	}

	close(h.closed)
	s.monitor.EgressEnded(h.req)

	s.mu.Lock()
	defer s.mu.Unlock()

	if isWeb(h.req) {
		s.handlingWeb = false
	}
	delete(s.activeHandlers, h.req.EgressId)
}

func (s *Manager) isIdle() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.activeHandlers) == 0
}

func (s *Manager) status() map[string]interface{} {
	info := map[string]interface{}{
		"CpuLoad": s.monitor.GetCPULoad(),
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, h := range s.activeHandlers {
		info[h.req.EgressId] = h.req.Request
	}
	return info
}

func (s *Manager) shutdown() {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, h := range s.activeHandlers {
		select {
		case <-h.closed:
		default:
			if err := h.cmd.Process.Signal(syscall.SIGINT); err != nil {
				logger.Errorw("failed to kill process", err, "egressID", h.req.EgressId)
			}
		}
	}
}

func isWeb(req *livekit.StartEgressRequest) bool {
	switch req.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite,
		*livekit.StartEgressRequest_Web:
		return true
	default:
		return false
	}
}
