package source

import (
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/livekit"
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

type SDKSource struct {
	room     *lksdk.Room
	logger   logger.Logger
	trackIDs []string
	active   atomic.Int32

	// track requests
	fileWriter *fileWriter

	// track composite requests
	audioSrc    *app.Source
	audioCodec  chan webrtc.RTPCodecParameters
	audioWriter *appWriter
	videoSrc    *app.Source
	videoCodec  chan webrtc.RTPCodecParameters
	videoWriter *appWriter

	// app source can't push data until pipeline is in playing state
	playing chan struct{}

	endRecording chan struct{}
}

func NewSDKSource(p *params.Params) (*SDKSource, error) {
	s := &SDKSource{
		room:         lksdk.CreateRoom(),
		logger:       p.Logger,
		endRecording: make(chan struct{}),
	}

	composite := false
	switch p.Info.Request.(type) {
	case *livekit.EgressInfo_TrackComposite:
		composite = true
		if p.AudioEnabled {
			src, err := app.NewAppSrc()
			if err != nil {
				return nil, err
			}
			s.audioSrc = src
			s.audioCodec = make(chan webrtc.RTPCodecParameters, 1)
			s.trackIDs = append(s.trackIDs, p.AudioTrackID)
		}
		if p.VideoEnabled {
			src, err := app.NewAppSrc()
			if err != nil {
				return nil, err
			}
			s.videoSrc = src
			s.videoCodec = make(chan webrtc.RTPCodecParameters, 1)
			s.trackIDs = append(s.trackIDs, p.VideoTrackID)
		}
		s.playing = make(chan struct{})

	case *livekit.EgressInfo_Track:
		s.trackIDs = []string{p.TrackID}
	}

	s.room.Callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		s.logger.Debugw("track subscribed", "trackID", track.ID())

		var err error
		if composite {
			switch track.Kind() {
			case webrtc.RTPCodecTypeAudio:
				s.audioCodec <- track.Codec()
				s.audioWriter, err = newAppWriter(track, rp, s.logger, s.audioSrc, s.playing)
			case webrtc.RTPCodecTypeVideo:
				s.videoCodec <- track.Codec()
				s.videoWriter, err = newAppWriter(track, rp, s.logger, s.videoSrc, s.playing)
			}
		} else {
			s.fileWriter, err = newFileWriter(track, rp, s.logger)
		}

		if err != nil {
			s.logger.Errorw("could not record track", err, "trackID", track.ID())
			return
		}
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

func (s *SDKSource) GetAudioSource() (*app.Source, chan webrtc.RTPCodecParameters) {
	return s.audioSrc, s.audioCodec
}

func (s *SDKSource) GetVideoSource() (*app.Source, chan webrtc.RTPCodecParameters) {
	return s.videoSrc, s.videoCodec
}

func (s *SDKSource) Playing() {
	select {
	case <-s.playing:
		return
	default:
		close(s.playing)
	}
}

func (s *SDKSource) StartRecording() chan struct{} {
	return nil
}

func (s *SDKSource) EndRecording() chan struct{} {
	return s.endRecording
}

func (s *SDKSource) Close() {
	if s.fileWriter != nil {
		s.fileWriter.stop()
	}

	s.room.Disconnect()
}
