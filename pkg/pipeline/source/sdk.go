package source

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/source/sdk"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	lksdk "github.com/livekit/server-sdk-go"
)

const (
	AudioAppSource = "audioAppSrc"
	VideoAppSource = "videoAppSrc"

	subscriptionTimeout = time.Second * 5
)

type SDKSource struct {
	room *lksdk.Room
	sync *sdk.Synchronizer

	// track
	trackID string

	// track composite
	audioTrackID string
	videoTrackID string

	// participant
	participantIdentity string

	audioWriter *sdk.AppWriter
	videoWriter *sdk.AppWriter

	active         atomic.Int32
	startRecording chan struct{}
	endRecording   chan struct{}

	onTrackMute func(bool)
}

func NewSDKSource(ctx context.Context, p *config.PipelineConfig) (*SDKSource, error) {
	ctx, span := tracer.Start(ctx, "SDKInput.New")
	defer span.End()

	startRecording := make(chan struct{})
	s := &SDKSource{
		sync: sdk.NewSynchronizer(func() {
			close(startRecording)
		}),
		startRecording: startRecording,
		endRecording:   make(chan struct{}),
	}

	if err := s.joinRoom(p); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SDKSource) StartRecording() chan struct{} {
	return s.startRecording
}

func (s *SDKSource) GetStartTime() int64 {
	return s.sync.GetStartedAt()
}

func (s *SDKSource) Playing(name string) {
	switch name {
	case AudioAppSource:
		s.audioWriter.Play()
	case VideoAppSource:
		s.videoWriter.Play()
	}
}

func (s *SDKSource) EndRecording() chan struct{} {
	return s.endRecording
}

func (s *SDKSource) GetEndTime() int64 {
	return s.sync.GetEndedAt()
}

func (s *SDKSource) CloseWriters() {
	s.sync.End()

	var wg sync.WaitGroup
	if s.audioWriter != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.audioWriter.Drain(false)
			logger.Debugw("audio writer finished")
		}()
	}
	if s.videoWriter != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.videoWriter.Drain(false)
			logger.Debugw("video writer finished")
		}()
	}
	wg.Wait()
}

func (s *SDKSource) StreamStopped(name string) {
	switch name {
	case AudioAppSource:
		s.audioWriter.Drain(true)
		if s.active.Dec() == 0 {
			s.onDisconnected()
		}
	case VideoAppSource:
		s.videoWriter.Drain(true)
		if s.active.Dec() == 0 {
			s.onDisconnected()
		}
	}
}

func (s *SDKSource) Close() {
	s.room.Disconnect()
}

func (s *SDKSource) joinRoom(p *config.PipelineConfig) error {
	cb := &lksdk.RoomCallback{
		OnDisconnected: s.onDisconnected,
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackMuted:       s.onTrackMuted,
			OnTrackUnmuted:     s.onTrackUnmuted,
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
		t := s.sync.AddTrack(track, rp.Identity())

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

		var codec types.MimeType
		var appSrcName string
		var err error
		writeBlanks := false

		switch {
		case strings.EqualFold(track.Codec().MimeType, string(types.MimeTypeOpus)):
			codec = types.MimeTypeOpus
			appSrcName = AudioAppSource
			p.AudioEnabled = true
			if p.AudioCodec == "" {
				p.AudioCodec = codec
			}
			if p.VideoEnabled {
				writeBlanks = true
			}

		case strings.EqualFold(track.Codec().MimeType, string(types.MimeTypeVP8)):
			codec = types.MimeTypeVP8
			appSrcName = VideoAppSource
			p.VideoEnabled = true

			if p.VideoCodec == "" {
				if p.AudioEnabled {
					// transcode to h264 for composite requests
					p.VideoCodec = types.MimeTypeH264
					p.VideoTranscoding = true
					writeBlanks = true
				} else {
					p.VideoCodec = types.MimeTypeVP8
				}
			}
			if p.TrackID != "" {
				if conf, ok := p.Outputs[types.EgressTypeFile]; ok {
					conf.OutputType = types.OutputTypeWebM
				}
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
		writer, err := sdk.NewAppWriter(track, rp, codec, appSrcName, s.sync, t, writeBlanks)
		if err != nil {
			logger.Errorw("could not create app writer", err)
			onSubscribeErr = err
			return
		}

		// write blank frames only when writing to mp4
		switch track.Kind() {
		case webrtc.RTPCodecTypeAudio:
			s.audioWriter = writer
		case webrtc.RTPCodecTypeVideo:
			s.videoWriter = writer
		}
	}

	s.room = lksdk.CreateRoom(cb)
	logger.Debugw("connecting to room")
	if err := s.room.JoinWithToken(p.WsUrl, p.Token, lksdk.WithAutoSubscribe(false)); err != nil {
		return err
	}

	var fileIdentifier string
	tracks := make(map[string]struct{})

	switch p.Info.Request.(type) {
	case *livekit.EgressInfo_TrackComposite:
		fileIdentifier = p.Info.RoomName
		if p.AudioEnabled {
			s.audioTrackID = p.AudioTrackID
			tracks[s.audioTrackID] = struct{}{}
		}
		if p.VideoEnabled {
			s.videoTrackID = p.VideoTrackID
			tracks[s.videoTrackID] = struct{}{}
		}

	case *livekit.EgressInfo_Track:
		fileIdentifier = p.TrackID
		s.trackID = p.TrackID
		tracks[s.trackID] = struct{}{}
	}

	wg.Add(len(tracks))
	if err := s.subscribeToTracks(tracks); err != nil {
		return err
	}
	wg.Wait()
	if onSubscribeErr != nil {
		return onSubscribeErr
	}

	if err := p.UpdateInfoFromSDK(fileIdentifier, filenameReplacements); err != nil {
		logger.Errorw("could not update file params", err)
		return err
	}

	return nil
}

func (s *SDKSource) subscribeToTracks(expecting map[string]struct{}) error {
	deadline := time.Now().Add(subscriptionTimeout)
	for time.Now().Before(deadline) {
		for _, p := range s.room.GetParticipants() {
			for _, track := range p.Tracks() {
				trackID := track.SID()
				if _, ok := expecting[trackID]; ok {
					if pub, ok := track.(*lksdk.RemoteTrackPublication); ok {
						pub.OnRTCP(s.sync.OnRTCP)
						err := pub.SetSubscribed(true)
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

func (s *SDKSource) SetOnTrackMute(onTrackMuted func(bool)) {
	s.onTrackMute = onTrackMuted
}

func (s *SDKSource) onTrackMuted(pub lksdk.TrackPublication, _ lksdk.Participant) {
	track := pub.Track()
	if track == nil {
		return
	}

	if w := s.getWriterForTrack(track.ID()); w != nil {
		w.SetTrackMuted(true)
	}

	if s.onTrackMute != nil {
		s.onTrackMute(true)
	}
}

func (s *SDKSource) onTrackUnmuted(pub lksdk.TrackPublication, _ lksdk.Participant) {
	track := pub.Track()
	if track == nil {
		return
	}

	if w := s.getWriterForTrack(track.ID()); w != nil {
		w.SetTrackMuted(false)
	}

	if s.onTrackMute != nil {
		s.onTrackMute(false)
	}
}

func (s *SDKSource) onTrackUnpublished(track *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
	if w := s.getWriterForTrack(track.SID()); w != nil {
		w.Drain(true)
		if s.active.Dec() == 0 {
			s.onDisconnected()
		}
	}
}

func (s *SDKSource) onDisconnected() {
	select {
	case <-s.endRecording:
		return
	default:
		close(s.endRecording)
	}
}

func (s *SDKSource) getWriterForTrack(trackID string) *sdk.AppWriter {
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
