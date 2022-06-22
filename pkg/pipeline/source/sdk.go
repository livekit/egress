package source

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/egress/pkg/tracer"
)

const (
	AudioAppSource = "audioAppSrc"
	VideoAppSource = "videoAppSrc"
)

type SDKSource struct {
	room   *lksdk.Room
	logger logger.Logger
	active atomic.Int32
	cs     *clockSync

	// track
	trackID string

	// track composite audio
	audioTrackID string
	audioSrc     *app.Source
	audioCodec   webrtc.RTPCodecParameters
	audioWriter  *appWriter
	audioPlaying chan struct{}

	// track composite video
	videoTrackID string
	videoSrc     *app.Source
	videoCodec   webrtc.RTPCodecParameters
	videoWriter  *appWriter
	videoPlaying chan struct{}

	mutedChan    chan bool
	endRecording chan struct{}
}

func NewSDKSource(ctx context.Context, p *params.Params) (*SDKSource, error) {
	ctx, span := tracer.Start(ctx, "SDKSource.New")
	defer span.End()

	s := &SDKSource{
		room:         lksdk.CreateRoom(),
		logger:       p.Logger,
		cs:           &clockSync{},
		mutedChan:    p.MutedChan,
		endRecording: make(chan struct{}),
	}

	var fileIdentifier string
	var wg sync.WaitGroup

	switch p.Info.Request.(type) {
	case *livekit.EgressInfo_TrackComposite:
		fileIdentifier = p.RoomName
		if p.AudioEnabled {
			s.audioTrackID = p.AudioTrackID
			wg.Add(1)
		}
		if p.VideoEnabled {
			s.videoTrackID = p.VideoTrackID
			wg.Add(1)
		}

	case *livekit.EgressInfo_Track:
		fileIdentifier = p.TrackID
		s.trackID = p.TrackID
		wg.Add(1)
	}

	s.room.Callback.OnTrackMuted = s.onTrackMuted
	s.room.Callback.OnTrackUnmuted = s.onTrackUnmuted
	s.room.Callback.OnTrackUnpublished = s.onTrackUnpublished
	s.room.Callback.OnDisconnected = s.onComplete

	var onSubscribeErr error
	s.room.Callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		defer wg.Done()
		s.logger.Debugw("track subscribed", "trackID", track.ID(), "mime", track.Codec().MimeType)

		var codec params.MimeType
		var appSrcName string
		var err error

		switch {
		case strings.EqualFold(track.Codec().MimeType, string(params.MimeTypeOpus)):
			codec = params.MimeTypeOpus
			appSrcName = AudioAppSource
			p.AudioEnabled = true
			if p.AudioCodec == "" {
				p.AudioCodec = codec
			}

		case strings.EqualFold(track.Codec().MimeType, string(params.MimeTypeVP8)):
			codec = params.MimeTypeVP8
			appSrcName = VideoAppSource
			p.VideoEnabled = true

			if p.VideoCodec == "" {
				if p.AudioEnabled {
					// transcode to h264 for composite requests
					p.VideoCodec = params.MimeTypeH264
				} else {
					p.VideoCodec = params.MimeTypeVP8
				}
			}
			if p.TrackID != "" {
				p.OutputType = params.OutputTypeIVF
			}

		case strings.EqualFold(track.Codec().MimeType, string(params.MimeTypeH264)):
			codec = params.MimeTypeH264
			appSrcName = VideoAppSource
			p.VideoEnabled = true

			if p.VideoCodec == "" {
				p.VideoCodec = params.MimeTypeH264
			}

		default:
			onSubscribeErr = errors.ErrNotSupported(track.Codec().MimeType)
			return
		}

		src, err := gst.NewElementWithName("appsrc", appSrcName)
		if err != nil {
			s.logger.Errorw("could not create appsrc", err)
			onSubscribeErr = err
			return
		}

		// write blank frames only when writing to mp4
		writeBlanks := p.VideoCodec == params.MimeTypeH264

		<-p.GstReady
		switch track.Kind() {
		case webrtc.RTPCodecTypeAudio:
			s.audioSrc = app.SrcFromElement(src)
			s.audioPlaying = make(chan struct{})
			s.audioCodec = track.Codec()
			s.audioWriter, err = newAppWriter(track, codec, rp, s.logger, s.audioSrc, s.cs, s.audioPlaying, writeBlanks)
			if err != nil {
				s.logger.Errorw("could not create app writer", err)
				onSubscribeErr = err
				return
			}

		case webrtc.RTPCodecTypeVideo:
			s.videoSrc = app.SrcFromElement(src)
			s.videoPlaying = make(chan struct{})
			s.videoCodec = track.Codec()
			s.videoWriter, err = newAppWriter(track, codec, rp, s.logger, s.videoSrc, s.cs, s.videoPlaying, writeBlanks)
			if err != nil {
				s.logger.Errorw("could not create app writer", err)
				onSubscribeErr = err
				return
			}
		}
	}

	if err := s.join(ctx, p); err != nil {
		return nil, err
	}

	wg.Wait()
	if onSubscribeErr != nil {
		return nil, onSubscribeErr
	}

	if p.EgressType == params.EgressTypeFile {
		if err := p.UpdateOutputTypeFromCodecs(fileIdentifier); err != nil {
			s.logger.Errorw("could not update file params", err)
			return nil, err
		}
	}

	return s, nil
}

func (s *SDKSource) join(ctx context.Context, p *params.Params) error {
	ctx, span := tracer.Start(ctx, "SDKSource.join")
	defer span.End()

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

func (s *SDKSource) onTrackMuted(pub lksdk.TrackPublication, _ lksdk.Participant) {
	track := pub.Track()
	if track == nil {
		return
	}

	if w := s.getWriterForTrack(track.ID()); w != nil {
		w.trackMuted()
	}

	// TODO: clean this up
	if s.mutedChan != nil {
		s.mutedChan <- false
	}
}

func (s *SDKSource) onTrackUnmuted(pub lksdk.TrackPublication, _ lksdk.Participant) {
	track := pub.Track()
	if track == nil {
		return
	}

	if w := s.getWriterForTrack(track.ID()); w != nil {
		w.trackUnmuted()
	}

	// TODO: clean this up
	if s.mutedChan != nil {
		s.mutedChan <- false
	}
}

func (s *SDKSource) onTrackUnpublished(track *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
	if w := s.getWriterForTrack(track.SID()); w != nil {
		w.sendEOS()
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

func (s *SDKSource) getWriterForTrack(trackID string) *appWriter {
	switch trackID {
	case s.trackID:
		if s.audioWriter != nil {
			return s.audioWriter
		} else if s.videoWriter != nil {
			return s.videoWriter
		}
	case s.audioTrackID:
		return s.audioWriter
	case s.videoTrackID:
		return s.videoWriter
	}

	return nil
}

func (s *SDKSource) StartRecording() chan struct{} {
	return nil
}

func (s *SDKSource) GetAudioSource() (*app.Source, webrtc.RTPCodecParameters) {
	return s.audioSrc, s.audioCodec
}

func (s *SDKSource) GetVideoSource() (*app.Source, webrtc.RTPCodecParameters) {
	return s.videoSrc, s.videoCodec
}

func (s *SDKSource) GetStartTime() int64 {
	return s.cs.startTime.Load()
}

func (s *SDKSource) GetEndTime() int64 {
	return s.cs.endTime.Load() + s.cs.delay.Load()
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

func (s *SDKSource) EndRecording() chan struct{} {
	return s.endRecording
}

func (s *SDKSource) SendEOS() {
	s.cs.SetEndTime(time.Now().UnixNano())

	var wg sync.WaitGroup
	if s.audioWriter != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.audioWriter.sendEOS()
		}()
	}
	if s.videoWriter != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.videoWriter.sendEOS()
		}()
	}
	wg.Wait()
}

func (s *SDKSource) Close() {
	s.room.Disconnect()
}
