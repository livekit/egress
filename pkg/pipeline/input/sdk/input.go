package sdk

import (
	"context"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/pipeline/input/builder"
	"github.com/livekit/egress/pkg/pipeline/params"
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

	room   *lksdk.Room
	logger logger.Logger
	cs     *synchronizer

	// track
	trackID string

	// track composite
	audioTrackID string
	videoTrackID string

	// participant
	participantIdentity string

	// composite audio source
	audioSrc         *app.Source
	audioCodec       webrtc.RTPCodecParameters
	audioWriter      *appWriter
	audioPlaying     chan struct{}
	audioParticipant string

	// composite video source
	videoSrc         *app.Source
	videoCodec       webrtc.RTPCodecParameters
	videoWriter      *appWriter
	videoPlaying     chan struct{}
	videoParticipant string

	active       atomic.Int32
	mutedChan    chan bool
	endRecording chan struct{}
}

func NewSDKInput(ctx context.Context, p *params.Params) (*SDKInput, error) {
	ctx, span := tracer.Start(ctx, "SDKInput.New")
	defer span.End()

	s := &SDKInput{
		logger:       p.Logger,
		cs:           &synchronizer{},
		mutedChan:    p.MutedChan,
		endRecording: make(chan struct{}),
	}

	if err := s.joinRoom(p); err != nil {
		return nil, err
	}

	input, err := builder.NewSDKInput(ctx, p, s.audioSrc, s.videoSrc, s.audioCodec, s.videoCodec)
	if err != nil {
		return nil, err
	}
	s.InputBin = input

	return s, nil
}

func (s *SDKInput) StartRecording() chan struct{} {
	return nil
}

func (s *SDKInput) GetStartTime() int64 {
	return s.cs.startTime.Load()
}

func (s *SDKInput) Playing(name string) {
	var playing chan struct{}

	if name == AudioAppSource {
		playing = s.audioPlaying
	} else if name == VideoAppSource {
		playing = s.videoPlaying
	} else {
		return
	}

	select {
	case <-playing:
		return
	default:
		close(playing)
	}
}

func (s *SDKInput) EndRecording() chan struct{} {
	return s.endRecording
}

func (s *SDKInput) GetEndTime() int64 {
	return s.cs.endTime.Load() + s.cs.delay.Load()
}

func (s *SDKInput) SendEOS() {
	s.cs.SetEndTime(time.Now().UnixNano())

	var wg sync.WaitGroup
	if s.audioWriter != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.audioWriter.sendEOS()
			s.logger.Debugw("audio writer finished")
		}()
	}
	if s.videoWriter != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.videoWriter.sendEOS()
			s.logger.Debugw("video writer finished")
		}()
	}
	wg.Wait()
}

func (s *SDKInput) SendAppSrcEOS(name string) {
	if name == AudioAppSource {
		s.audioWriter.sendEOS()
		if s.active.Dec() == 0 {
			s.onDisconnected()
		}
	} else if name == VideoAppSource {
		s.videoWriter.sendEOS()
		if s.active.Dec() == 0 {
			s.onDisconnected()
		}
	}
}

func (s *SDKInput) Close() {
	s.room.Disconnect()
}
