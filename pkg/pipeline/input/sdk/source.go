package sdk

import (
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"
)

func (s *SDKInput) joinRoom(p *config.PipelineConfig) error {
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
		logger.Debugw("track subscribed", "trackID", track.ID(), "mime", track.Codec().MimeType)
		s.active.Inc()

		var codec types.MimeType
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
		case strings.EqualFold(track.Codec().MimeType, string(types.MimeTypeOpus)):
			codec = types.MimeTypeOpus
			appSrcName = AudioAppSource
			p.AudioEnabled = true
			if p.AudioCodec == "" {
				p.AudioCodec = codec
			}

		case strings.EqualFold(track.Codec().MimeType, string(types.MimeTypeVP8)):
			codec = types.MimeTypeVP8
			appSrcName = VideoAppSource
			p.VideoEnabled = true

			if p.VideoCodec == "" {
				if p.AudioEnabled {
					// transcode to h264 for composite requests
					p.VideoCodec = types.MimeTypeH264
				} else {
					p.VideoCodec = types.MimeTypeVP8
				}
			}
			if p.TrackID != "" {
				p.OutputType = types.OutputTypeWebM
			}

		case strings.EqualFold(track.Codec().MimeType, string(types.MimeTypeH264)):
			codec = types.MimeTypeH264
			appSrcName = VideoAppSource
			p.VideoEnabled = true

			if p.VideoCodec == "" {
				p.VideoCodec = types.MimeTypeH264
			}

		default:
			onSubscribeErr = errors.ErrNotSupported(track.Codec().MimeType)
			return
		}

		<-p.GstReady
		src, err := gst.NewElementWithName("appsrc", appSrcName)
		if err != nil {
			logger.Errorw("could not create appsrc", err)
			onSubscribeErr = err
			return
		}

		// write blank frames only when writing to mp4
		writeBlanks := p.VideoCodec == types.MimeTypeH264

		switch track.Kind() {
		case webrtc.RTPCodecTypeAudio:
			s.audioSrc = app.SrcFromElement(src)
			s.audioPlaying = make(chan struct{})
			s.audioCodec = track.Codec()
			s.audioWriter, err = newAppWriter(track, codec, rp, s.audioSrc, s.cs, s.audioPlaying, writeBlanks)
			s.audioParticipant = rp.Identity()
			if err != nil {
				logger.Errorw("could not create app writer", err)
				onSubscribeErr = err
				return
			}

		case webrtc.RTPCodecTypeVideo:
			s.videoSrc = app.SrcFromElement(src)
			s.videoPlaying = make(chan struct{})
			s.videoCodec = track.Codec()
			s.videoWriter, err = newAppWriter(track, codec, rp, s.videoSrc, s.cs, s.videoPlaying, writeBlanks)
			s.videoParticipant = rp.Identity()
			if err != nil {
				logger.Errorw("could not create app writer", err)
				onSubscribeErr = err
				return
			}
		}
	}

	s.room = lksdk.CreateRoom(cb)
	logger.Debugw("connecting to room")
	if err := s.room.JoinWithToken(p.WsUrl, p.Token, lksdk.WithAutoSubscribe(false)); err != nil {
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
	case types.EgressTypeFile:
		if err := p.UpdateFileInfoFromSDK(fileIdentifier, filenameReplacements); err != nil {
			logger.Errorw("could not update file params", err)
			return err
		}
	case types.EgressTypeSegmentedFile:
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

	logger.Infow("participant published new track", "identity", s.participantIdentity, "trackID", pub.SID())
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
