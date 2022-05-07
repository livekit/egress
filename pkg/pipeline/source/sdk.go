package source

import (
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"
)

const (
	AudioAppSource = "audioAppSrc"
	VideoAppSource = "videoAppSrc"

	maxVideoLate = 1000 // nearly 2s for fhd video
	maxAudioLate = 200  // 4s for audio
)

type SDKSource struct {
	room   *lksdk.Room
	logger logger.Logger
	active atomic.Int32
	cs     *clockSync

	// track
	trackID    string
	fileWriter *fileWriter
	filePath   string

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

	// app source can't push data until pipeline is in playing state
	endRecording chan struct{}
	closed       chan struct{}
}

func NewSDKSource(p *params.Params) (*SDKSource, error) {
	s := &SDKSource{
		room:         lksdk.CreateRoom(),
		logger:       p.Logger,
		cs:           &clockSync{},
		endRecording: make(chan struct{}),
		closed:       make(chan struct{}),
	}

	var wg sync.WaitGroup
	isCompositeRequest := false
	switch p.Info.Request.(type) {
	case *livekit.EgressInfo_TrackComposite:
		isCompositeRequest = true
		if p.AudioEnabled {
			s.audioTrackID = p.AudioTrackID
			wg.Add(1)
		}
		if p.VideoEnabled {
			s.videoTrackID = p.VideoTrackID
			wg.Add(1)
		}

	case *livekit.EgressInfo_Track:
		s.trackID = p.TrackID
		wg.Add(1)
	}

	var onSubscribeErr error
	s.room.Callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		defer wg.Done()
		s.logger.Debugw("track subscribed", "trackID", track.ID())

		switch track.Kind() {
		case webrtc.RTPCodecTypeAudio:
			src, err := gst.NewElementWithName("appsrc", AudioAppSource)
			if err != nil {
				s.logger.Errorw("could not create appsrc", err)
				onSubscribeErr = err
				return
			}

			s.audioSrc = app.SrcFromElement(src)
			s.audioPlaying = make(chan struct{})

			s.audioCodec = track.Codec()
			s.audioWriter, err = newAppWriter(track, rp, s.logger, s.audioSrc, s.cs, s.audioPlaying)
			if err != nil {
				s.logger.Errorw("could not create app writer", err)
				onSubscribeErr = err
				return
			}

			// update params for track egress
			if !isCompositeRequest {
				p.AudioEnabled = true
				p.AudioCodec = livekit.AudioCodec_OPUS
				p.FileType = livekit.EncodedFileType_OGG
				if err = p.UpdateFilename(track); err != nil {
					s.logger.Errorw("could not update filename", err)
					onSubscribeErr = err
					return
				}
			}

		case webrtc.RTPCodecTypeVideo:
			if isCompositeRequest {
				var src *gst.Element
				src, err := gst.NewElementWithName("appsrc", VideoAppSource)
				if err != nil {
					s.logger.Errorw("could not create appsrc", err)
					onSubscribeErr = err
					return
				}

				s.videoSrc = app.SrcFromElement(src)
				s.videoPlaying = make(chan struct{})

				s.videoCodec = track.Codec()
				s.videoWriter, err = newAppWriter(track, rp, s.logger, s.videoSrc, s.cs, s.videoPlaying)
				if err != nil {
					s.logger.Errorw("could not create app writer", err)
					onSubscribeErr = err
					return
				}

			} else {
				// update params for track egress
				p.SkipPipeline = true
				p.VideoEnabled = true

				// Update filename only if it's not in WS mode since WS doesn't produce a file
				if p.WebSocketEgressUrl == "" {
					if err := p.UpdateFilename(track); err != nil {
						s.logger.Errorw("could not update filename", err)
						onSubscribeErr = err
						return
					}
				}

				// If we want to do WS track egress, make sure the mime type is audio/opus.
				// Else, it's not supported
				if p.WebSocketEgressUrl != "" && track.Codec().MimeType != webrtc.MimeTypeOpus {
					s.logger.Errorw("cannot fulfil track egress request", errors.ErrNotSupported(track.Codec().MimeType))
				}

				var err error
				s.fileWriter, err = newFileWriter(p, track, rp, s.logger, s.cs)
				if onSubscribeErr != nil {
					s.logger.Errorw("could not create file writer", err)
					onSubscribeErr = err
					return
				}
			}
		}
	}

	// Track WS notification
	s.room.Callback.OnTrackMuted = func(_ lksdk.TrackPublication, rp lksdk.Participant) {
		if p.MutedChan != nil {
			p.MutedChan <- true
		}
	}
	s.room.Callback.OnTrackUnmuted = func(_ lksdk.TrackPublication, rp lksdk.Participant) {
		if p.MutedChan != nil {
			p.MutedChan <- false
		}
	}

	if err := s.join(p); err != nil {
		return nil, err
	}

	wg.Wait()
	return s, onSubscribeErr
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
		if s.fileWriter != nil {
			s.fileWriter.stop()
		} else {
			s.audioWriter.stop()
		}
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

func (s *SDKSource) StartRecording() chan struct{} {
	return nil
}

func (s *SDKSource) EndRecording() chan struct{} {
	return s.endRecording
}

func (s *SDKSource) SendEOS() {
	select {
	case <-s.closed:
		return
	default:
		close(s.closed)

		s.cs.SetEndTime(time.Now().UnixNano())

		if s.fileWriter != nil {
			s.fileWriter.stop()
		} else {
			var wg sync.WaitGroup
			if s.audioWriter != nil {
				wg.Add(1)
				go func() {
					defer wg.Done()
					s.audioWriter.stop()
				}()
			}
			if s.videoWriter != nil {
				wg.Add(1)
				go func() {
					defer wg.Done()
					s.videoWriter.stop()
				}()
			}
			wg.Wait()
		}
	}
}

func (s *SDKSource) Close() {
	s.room.Disconnect()
}
