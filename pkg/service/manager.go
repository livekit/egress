package service

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"sync"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/protocol/utils"
)

type ProcessManager struct {
	conf    *config.ServiceConfig
	monitor *stats.Monitor

	mu                            sync.RWMutex
	handlingWeb                   bool
	activeHandlers                map[string]*process
	onFatal                       func()
	currentHandlerDebugPortOffset int
}

type process struct {
	handlerID  string
	req        *livekit.StartEgressRequest
	cmd        *exec.Cmd
	grpcClient ipc.EgressHandlerClient
	closed     chan struct{}
}

func NewProcessManager(conf *config.ServiceConfig, monitor *stats.Monitor) *ProcessManager {
	return &ProcessManager{
		conf:           conf,
		monitor:        monitor,
		activeHandlers: make(map[string]*process),
	}
}

func (s *ProcessManager) onFatalError(f func()) {
	s.onFatal = f
}

func (s *ProcessManager) canAccept(req *livekit.StartEgressRequest) bool {
	return !s.handlingWeb && (!isWeb(req) || s.isIdle())
}

func (s *ProcessManager) launchHandler(req *livekit.StartEgressRequest) error {
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

	options := []string{
		"run-handler",
		"--config", string(confString),
		"--request", string(reqString),
	}

	var debugHandlerPort int
	if s.conf.DebugHandlerPort > 0 && s.conf.DebugHandlerPort < 65535-1000 {
		debugHandlerPort = s.conf.DebugHandlerPort + 1 + s.currentHandlerDebugPortOffset

		options = append(options, "--debug-port", fmt.Sprintf("%d", debugHandlerPort))
		s.currentHandlerDebugPortOffset = (s.currentHandlerDebugPortOffset + 1) % 1000
	}

	cmd := exec.Command("egress",
		options...,
	)
	cmd.Dir = "/"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err = cmd.Start(); err != nil {
		span.RecordError(err)
		logger.Errorw("could not launch process", err)
		return err
	}

	s.monitor.EgressStarted(req)
	h := &process{
		handlerID: handlerID,
		req:       req,
		cmd:       cmd,
		closed:    make(chan struct{}),
	}

	socketAddr := getSocketAddress(handlerID)
	conn, err := grpc.Dial(socketAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(_ context.Context, addr string) (net.Conn, error) {
			return net.Dial(network, addr)
		}),
	)
	if err != nil {
		span.RecordError(err)
		logger.Errorw("could not dial grpc handler", err)
		return err
	}
	h.grpcClient = ipc.NewEgressHandlerClient(conn)

	s.mu.Lock()
	s.activeHandlers[req.EgressId] = h
	s.mu.Unlock()

	go s.awaitCleanup(h)

	return nil
}

func (s *ProcessManager) awaitCleanup(h *process) {
	if err := h.cmd.Wait(); err != nil {
		logger.Errorw("process failed", err)
		if s.onFatal != nil {
			s.onFatal()
		}
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

func (s *ProcessManager) isIdle() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.activeHandlers) == 0
}

func (s *ProcessManager) status() map[string]interface{} {
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

func (s *ProcessManager) listEgress() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	egressIDs := make([]string, 0, len(s.activeHandlers))
	for egressID := range s.activeHandlers {
		egressIDs = append(egressIDs, egressID)
	}
	return egressIDs
}

func (s *ProcessManager) sendGrpcDebugRequest(egressId string, req *ipc.GetDebugInfoRequest) (*ipc.GetDebugInfoResponse, error) {
	s.mu.Lock()

	h, ok := s.activeHandlers[egressId]
	if !ok {
		s.mu.Unlock()
		return nil, errors.ErrEgressNotFound
	}
	c := h.grpcClient
	s.mu.Unlock()

	return c.GetDebugInfo(context.Background(), req)
}

func (s *ProcessManager) shutdown() {
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

func getSocketAddress(handlerID string) string {
	return fmt.Sprintf("/tmp/%s.sock", handlerID)
}
