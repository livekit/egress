package sdk

import (
	"context"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/input/builder"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	lksdk "github.com/livekit/server-sdk-go"
)

const (
	AudioAppSource = "audioAppSrc"
	VideoAppSource = "videoAppSrc"

	subscriptionTimeout = time.Second * 5
)

type SDKInput struct {
	*builder.InputBin

	room *lksdk.Room
	sync *synchronizer

	// track
	trackID string

	// track composite
	audioTrackID string
	videoTrackID string

	// participant
	participantIdentity string

	audioWriter *appWriter
	videoWriter *appWriter

	active         atomic.Int32
	mutedChan      chan bool
	startRecording chan struct{}
	endRecording   chan struct{}
}

func NewSDKInput(ctx context.Context, p *config.PipelineConfig) (*SDKInput, error) {
	ctx, span := tracer.Start(ctx, "SDKInput.New")
	defer span.End()

	startRecording := make(chan struct{})
	s := &SDKInput{
		sync: newSynchronizer(func() {
			close(startRecording)
		}),
		mutedChan:      p.MutedChan,
		startRecording: startRecording,
		endRecording:   make(chan struct{}),
	}

	if err := s.joinRoom(p); err != nil {
		return nil, err
	}

	var audioSrc, videoSrc *app.Source
	var audioCodec, videoCodec webrtc.RTPCodecParameters
	if s.audioWriter != nil {
		audioSrc = s.audioWriter.src
		audioCodec = s.audioWriter.track.Codec()
	}
	if s.videoWriter != nil {
		videoSrc = s.videoWriter.src
		videoCodec = s.videoWriter.track.Codec()
	}
	input, err := builder.NewSDKInput(ctx, p, audioSrc, videoSrc, audioCodec, videoCodec)
	if err != nil {
		return nil, err
	}
	s.InputBin = input

	return s, nil
}

func (s *SDKInput) StartRecording() chan struct{} {
	return s.startRecording
}

func (s *SDKInput) GetStartTime() int64 {
	return s.sync.getStartedAt()
}

func (s *SDKInput) Playing(name string) {
	switch name {
	case AudioAppSource:
		s.audioWriter.play()
	case VideoAppSource:
		s.videoWriter.play()
	}
}

func (s *SDKInput) EndRecording() chan struct{} {
	return s.endRecording
}

func (s *SDKInput) GetEndTime() int64 {
	return s.sync.getEndedAt()
}

func (s *SDKInput) CloseWriters() {
	s.sync.end()

	var wg sync.WaitGroup
	if s.audioWriter != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.audioWriter.drain(false)
			logger.Debugw("audio writer finished")
		}()
	}
	if s.videoWriter != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.videoWriter.drain(false)
			logger.Debugw("video writer finished")
		}()
	}
	wg.Wait()
}

func (s *SDKInput) StreamStopped(name string) {
	switch name {
	case AudioAppSource:
		s.audioWriter.drain(true)
		if s.active.Dec() == 0 {
			s.onDisconnected()
		}
	case VideoAppSource:
		s.videoWriter.drain(true)
		if s.active.Dec() == 0 {
			s.onDisconnected()
		}
	}
}

func (s *SDKInput) Close() {
	s.room.Disconnect()
}
