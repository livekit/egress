package source

import (
	"sync"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
)

const (
	MimeTypeOpus = "audio/opus"
	MimeTypeH264 = "video/h264"
	MimeTypeVP8  = "video/vp8"

	maxVideoLate = 1000 // nearly 2s for fhd video
	maxAudioLate = 200  // 4s for audio
)

type writer interface {
	start()
	stop()
}

type SDKSource struct {
	mu sync.Mutex

	room     *lksdk.Room
	trackIDs []string
	active   atomic.Int32
	writers  map[string]writer

	ready        chan struct{}
	endRecording chan struct{}

	logger logger.Logger
}

func NewSDKAppSource(p *params.Params, audioSrc, videoSrc *app.Source, audioMimeType, videoMimeType chan string) (*SDKSource, error) {
	s := &SDKSource{
		room:         lksdk.CreateRoom(),
		writers:      make(map[string]writer),
		ready:        make(chan struct{}),
		endRecording: make(chan struct{}),
		logger:       p.Logger,
	}

	if p.AudioEnabled {
		s.trackIDs = append(s.trackIDs, p.AudioTrackID)
	}
	if p.VideoEnabled {
		s.trackIDs = append(s.trackIDs, p.VideoTrackID)
	}

	s.room.Callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		w, err := newAppWriter(track, rp, s.logger, audioSrc, videoSrc, audioMimeType, videoMimeType, s.ready)
		if err != nil {
			s.logger.Errorw("could not record track", err, "trackID", track.ID())
			return
		}

		go w.start()

		s.mu.Lock()
		s.writers[track.ID()] = w
		s.mu.Unlock()
	}

	if err := s.join(p); err != nil {
		return nil, err
	}

	return s, nil
}

func NewSDKFileSource(p *params.Params) (*SDKSource, error) {
	s := &SDKSource{
		room:         lksdk.CreateRoom(),
		trackIDs:     []string{p.TrackID},
		writers:      make(map[string]writer),
		endRecording: make(chan struct{}),
		logger:       p.Logger,
	}

	s.room.Callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		w, err := newFileWriter(track, rp, s.logger)
		if err != nil {
			s.logger.Errorw("could not record track", err, "trackID", track.ID())
			return
		}

		go w.start()

		s.mu.Lock()
		s.writers[track.ID()] = w
		s.mu.Unlock()
	}

	if err := s.join(p); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *SDKSource) join(p *params.Params) error {
	s.room.Callback.OnTrackUnpublished = s.onTrackUnpublished
	s.room.Callback.OnDisconnected = s.onComplete

	s.logger.Debugw("connecting to room")
	if err := s.room.JoinWithToken(p.LKUrl, p.Token, lksdk.WithAutoSubscribe(false)); err != nil {
		return err
	}

	expecting := make(map[string]bool)
	for _, trackID := range s.trackIDs {
		expecting[trackID] = true
	}

	for _, p := range s.room.GetParticipants() {
		for _, track := range p.Tracks() {
			if expecting[track.SID()] {
				if rt, ok := track.(*lksdk.RemoteTrackPublication); ok {
					err := rt.SetSubscribed(true)
					if err != nil {
						return err
					}

					delete(expecting, track.SID())
					s.active.Inc()
					if len(expecting) == 0 {
						return nil
					}
				}
			}
		}
	}

	for trackID := range expecting {
		return errors.ErrTrackNotFound(trackID)
	}

	return nil
}

func (s *SDKSource) onTrackUnpublished(track *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
	for _, trackID := range s.trackIDs {
		if track.SID() == trackID {
			if s.active.Dec() == 0 {
				s.onComplete()
			}
			return
		}
	}
}

func (s *SDKSource) onComplete() {
	select {
	case <-s.endRecording:
		return
	default:
		close(s.endRecording)
	}
}

func (s *SDKSource) Ready() {
	select {
	case <-s.ready:
		return
	default:
		close(s.ready)
	}
}

func (s *SDKSource) StartRecording() chan struct{} {
	return nil
}

func (s *SDKSource) EndRecording() chan struct{} {
	return s.endRecording
}

func (s *SDKSource) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.writers {
		w.stop()
	}
	s.room.Disconnect()
}
