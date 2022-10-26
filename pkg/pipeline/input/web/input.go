package web

import (
	"context"
	"math/rand"
	"os"
	"os/exec"
	"time"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/input/builder"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

const (
	startRecordingLog = "START_RECORDING"
	endRecordingLog   = "END_RECORDING"
)

type WebInput struct {
	*builder.InputBin

	pulseSink    string
	xvfb         *exec.Cmd
	chromeCancel context.CancelFunc

	startRecording chan struct{}
	endRecording   chan struct{}

	logger logger.Logger
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

func NewWebInput(ctx context.Context, conf *config.Config, p *params.Params) (*WebInput, error) {
	ctx, span := tracer.Start(ctx, "WebInput.New")
	defer span.End()

	s := &WebInput{
		logger: p.Logger,
	}

	if err := s.createPulseSink(ctx, p); err != nil {
		s.logger.Errorw("failed to load pulse sink", err)
		s.Close()
		return nil, err
	}

	if err := s.launchXvfb(ctx, p); err != nil {
		s.logger.Errorw("failed to launch xvfb", err)
		s.Close()
		return nil, err
	}

	if err := s.launchChrome(ctx, p, conf.Insecure); err != nil {
		s.logger.Errorw("failed to launch chrome", err, "display", p.Display)
		s.Close()
		return nil, err
	}

	<-p.GstReady
	input, err := builder.NewWebInput(ctx, p)
	if err != nil {
		s.logger.Errorw("failed to build input bin", err)
		s.Close()
		return nil, err
	}
	s.InputBin = input

	return s, nil
}

func (s *WebInput) StartRecording() chan struct{} {
	return s.startRecording
}

func (s *WebInput) EndRecording() chan struct{} {
	return s.endRecording
}

func (s *WebInput) Close() {
	if s.chromeCancel != nil {
		s.chromeCancel()
		s.chromeCancel = nil
	}

	if s.xvfb != nil {
		err := s.xvfb.Process.Signal(os.Interrupt)
		if err != nil {
			s.logger.Errorw("failed to kill xvfb", err)
		}
		s.xvfb = nil
	}

	if s.pulseSink != "" {
		err := exec.Command("pactl", "unload-module", s.pulseSink).Run()
		if err != nil {
			s.logger.Errorw("failed to unload pulse sink", err)
		}
	}
}
