package source

import (
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
)

const (
	AudioAppSource = "audioAppSrc"
	VideoAppSource = "videoAppSrc"

	MimeTypeOpus = "audio/opus"
	MimeTypeH264 = "video/h264"
	MimeTypeVP8  = "video/vp8"

	maxVideoLate = 1000 // nearly 2s for fhd video
	maxAudioLate = 200  // 4s for audio
)

type SDKSource struct {
	room   *lksdk.Room
	logger logger.Logger
	active atomic.Int32

	// track requests
	trackID    string
	fileWriter *fileWriter

	// track composite audio
	audioTrackID string
	audioSrc     *app.Source
	audioCodec   chan webrtc.RTPCodecParameters
	audioWriter  *appWriter
	audioPlaying chan struct{}

	// track composite video
	videoTrackID string
	videoSrc     *app.Source
	videoCodec   chan webrtc.RTPCodecParameters
	videoWriter  *appWriter
	videoPlaying chan struct{}

	// app source can't push data until pipeline is in playing state
	endRecording chan struct{}
	closed       chan struct{}
}

func NewSDKSource(p *params.Params) (*SDKSource, error) {
	s := &SDKSource{
		room:         lksdk.CreateRoom(),
		logger:       p.Logger,
		endRecording: make(chan struct{}),
		closed:       make(chan struct{}),
	}

	composite := false
	switch p.Info.Request.(type) {
	case *livekit.EgressInfo_TrackComposite:
		composite = true
		if p.AudioEnabled {
			src, err := gst.NewElementWithName("appsrc", AudioAppSource)
			if err != nil {
				return nil, err
			}

			s.audioSrc = app.SrcFromElement(src)
			s.audioCodec = make(chan webrtc.RTPCodecParameters, 1)
			s.audioPlaying = make(chan struct{})
			s.audioTrackID = p.AudioTrackID
		}
		if p.VideoEnabled {
			src, err := gst.NewElementWithName("appsrc", VideoAppSource)
			if err != nil {
				return nil, err
			}

			s.videoSrc = app.SrcFromElement(src)
			s.videoCodec = make(chan webrtc.RTPCodecParameters, 1)
			s.videoPlaying = make(chan struct{})
			s.videoTrackID = p.VideoTrackID
		}

	case *livekit.EgressInfo_Track:
		s.trackID = p.TrackID
	}

	cs := &clockSync{}
	s.room.Callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		s.logger.Debugw("track subscribed", "trackID", track.ID())

		var err error
		if composite {
			switch track.Kind() {
			case webrtc.RTPCodecTypeAudio:
				s.audioCodec <- track.Codec()
				s.audioWriter, err = newAppWriter(track, rp, s.logger, s.audioSrc, cs, s.audioPlaying)
			case webrtc.RTPCodecTypeVideo:
				s.videoCodec <- track.Codec()
				s.videoWriter, err = newAppWriter(track, rp, s.logger, s.videoSrc, cs, s.videoPlaying)
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
	if s.trackID != "" {
		expecting[s.trackID] = true
	} else {
		if s.audioTrackID != "" {
			expecting[s.audioTrackID] = true
		}
		if s.videoTrackID != "" {
			expecting[s.videoTrackID] = true
		}
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
	switch track.SID() {
	case s.trackID:
		s.fileWriter.stop()
	case s.audioTrackID:
		s.audioWriter.stop()
	case s.videoTrackID:
		s.videoWriter.stop()
	}

	if s.active.Dec() == 0 {
		s.onComplete()
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

func (s *SDKSource) Playing(name string) {
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

func (s *SDKSource) StartRecording() chan struct{} {
	return nil
}

func (s *SDKSource) EndRecording() chan struct{} {
	return s.endRecording
}

func (s *SDKSource) Close() {
	select {
	case <-s.closed:
		return
	default:
		close(s.closed)

		if s.fileWriter != nil {
			s.fileWriter.stop()
		}
		if s.audioWriter != nil {
			s.audioWriter.stop()
		}
		if s.videoWriter != nil {
			s.videoWriter.stop()
		}

		s.room.Disconnect()
	}
}
