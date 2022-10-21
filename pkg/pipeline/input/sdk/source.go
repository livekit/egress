package sdk

import (
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go"
)

func (s *SDKInput) joinRoom(p *params.Params) error {
	cb := &lksdk.RoomCallback{
		OnDisconnected:            s.onDisconnected,
		OnParticipantDisconnected: s.onParticipantDisconnected,
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackMuted:       s.onTrackMuted,
			OnTrackUnmuted:     s.onTrackUnmuted,
			OnTrackPublished:   s.onTrackPublished,
			OnTrackUnpublished: s.onTrackUnpublished,
		},
	}

	var mu sync.Mutex
	filenameReplacements := make(map[string]string)

	var onSubscribeErr error
	var wg sync.WaitGroup
	cb.OnTrackSubscribed = func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		defer wg.Done()
		s.logger.Debugw("track subscribed", "trackID", track.ID(), "mime", track.Codec().MimeType)
		s.active.Inc()

		var codec params.MimeType
		var appSrcName string
		var err error

		mu.Lock()
		if p.ParticipantIdentity == "" || track.Kind() == webrtc.RTPCodecTypeVideo {
			p.ParticipantIdentity = rp.Identity()
			filenameReplacements["{publisher_identity}"] = p.ParticipantIdentity
		}

		if p.TrackID != "" {
			if track.Kind() == webrtc.RTPCodecTypeAudio {
				p.TrackKind = "audio"
			} else {
				p.TrackKind = "video"
			}
			p.TrackSource = strings.ToLower(pub.Source().String())

			filenameReplacements["{track_id}"] = p.TrackID
			filenameReplacements["{track_type}"] = p.TrackKind
			filenameReplacements["{track_source}"] = p.TrackSource
		}
		mu.Unlock()

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
				p.OutputType = params.OutputTypeWebM
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

		<-p.GstReady
		src, err := gst.NewElementWithName("appsrc", appSrcName)
		if err != nil {
			s.logger.Errorw("could not create appsrc", err)
			onSubscribeErr = err
			return
		}

		// write blank frames only when writing to mp4
		writeBlanks := p.VideoCodec == params.MimeTypeH264

		switch track.Kind() {
		case webrtc.RTPCodecTypeAudio:
			s.audioSrc = app.SrcFromElement(src)
			s.audioPlaying = make(chan struct{})
			s.audioCodec = track.Codec()
			s.audioWriter, err = newAppWriter(track, codec, rp, s.logger, s.audioSrc, s.cs, s.audioPlaying, writeBlanks)
			s.audioParticipant = rp.Identity()
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
			s.videoParticipant = rp.Identity()
			if err != nil {
				s.logger.Errorw("could not create app writer", err)
				onSubscribeErr = err
				return
			}
		}
	}

	s.room = lksdk.CreateRoom(cb)
	s.logger.Debugw("connecting to room")
	if err := s.room.JoinWithToken(p.LKUrl, p.Token, lksdk.WithAutoSubscribe(false)); err != nil {
		return err
	}

	var fileIdentifier string
	switch p.Info.Request.(type) {
	case *livekit.EgressInfo_TrackComposite:
		fileIdentifier = p.Info.RoomName
		tracks := make(map[string]struct{})
		if p.AudioEnabled {
			s.audioTrackID = p.AudioTrackID
			tracks[s.audioTrackID] = struct{}{}
		}
		if p.VideoEnabled {
			s.videoTrackID = p.VideoTrackID
			tracks[s.videoTrackID] = struct{}{}
		}
		wg.Add(len(tracks))
		if err := s.subscribeToTracks(tracks); err != nil {
			return err
		}

	case *livekit.EgressInfo_Track:
		fileIdentifier = p.TrackID
		s.trackID = p.TrackID
		wg.Add(1)
		if err := s.subscribeToTracks(map[string]struct{}{s.trackID: {}}); err != nil {
			return err
		}
	}

	wg.Wait()
	if onSubscribeErr != nil {
		return onSubscribeErr
	}

	switch p.EgressType {
	case params.EgressTypeFile:
		if err := p.UpdateFileInfoFromSDK(fileIdentifier, filenameReplacements); err != nil {
			s.logger.Errorw("could not update file params", err)
			return err
		}
	case params.EgressTypeSegmentedFile:
		p.UpdatePlaylistNamesFromSDK(filenameReplacements)
	}

	return nil
}

func (s *SDKInput) onParticipantDisconnected(p *lksdk.RemoteParticipant) {
	identity := p.Identity()
	if identity == s.audioParticipant {
		go s.SendAppSrcEOS(AudioAppSource)
	}
	if identity == s.videoParticipant {
		go s.SendAppSrcEOS(VideoAppSource)
	}
}

func (s *SDKInput) onTrackPublished(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	if rp.Identity() != s.participantIdentity {
		return
	}

	s.logger.Errorw("participant published new track", nil, "identity", s.participantIdentity, "trackID", pub.SID())
}

func (s *SDKInput) onTrackMuted(pub lksdk.TrackPublication, _ lksdk.Participant) {
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

func (s *SDKInput) onTrackUnmuted(pub lksdk.TrackPublication, _ lksdk.Participant) {
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

func (s *SDKInput) onTrackUnpublished(track *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
	if w := s.getWriterForTrack(track.SID()); w != nil {
		w.sendEOS()
	}
}

func (s *SDKInput) onDisconnected() {
	select {
	case <-s.endRecording:
		return
	default:
		close(s.endRecording)
	}
}

func (s *SDKInput) subscribeToParticipant(wg *sync.WaitGroup) error {
	deadline := time.Now().Add(subscriptionTimeout)
	for time.Now().Before(deadline) {
		for _, p := range s.room.GetParticipants() {
			if p.Identity() == s.participantIdentity {
				for _, track := range p.Tracks() {
					wg.Add(1)
					if rt, ok := track.(*lksdk.RemoteTrackPublication); ok {
						err := rt.SetSubscribed(true)
						if err != nil {
							return err
						}
					}
				}

				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	return errors.ErrParticipantNotFound(s.participantIdentity)
}

func (s *SDKInput) subscribeToTracks(expecting map[string]struct{}) error {
	deadline := time.Now().Add(subscriptionTimeout)
	for time.Now().Before(deadline) {
		for _, p := range s.room.GetParticipants() {
			for _, track := range p.Tracks() {
				if _, ok := expecting[track.SID()]; ok {
					if rt, ok := track.(*lksdk.RemoteTrackPublication); ok {
						err := rt.SetSubscribed(true)
						if err != nil {
							return err
						}

						delete(expecting, track.SID())
						if len(expecting) == 0 {
							return nil
						}
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	for trackID := range expecting {
		return errors.ErrTrackNotFound(trackID)
	}

	return nil
}

func (s *SDKInput) getWriterForTrack(trackID string) *appWriter {
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
