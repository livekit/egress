package source

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/source/sdk"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/livekit/server-sdk-go/pkg/synchronizer"
)

const (
	AudioAppSource = "audioAppSrc"
	VideoAppSource = "videoAppSrc"

	subscriptionTimeout = time.Second * 30

	numTracks = 1
)

type SDKSource struct {
	*config.Callbacks

	room *lksdk.Room
	sync *synchronizer.Synchronizer

	// track
	trackID string

	// track composite
	audioTrackID string
	videoTrackID string

	// participant
	identity string

	audioWriter *sdk.AppWriter
	videoWriter *sdk.AppWriter

	initialized    core.Fuse
	subscriptions  sync.WaitGroup
	active         atomic.Int32
	startRecording chan struct{}
	endRecording   chan struct{}
}

func NewSDKSource(ctx context.Context, p *config.PipelineConfig) (*SDKSource, error) {
	ctx, span := tracer.Start(ctx, "SDKInput.New")
	defer span.End()

	startRecording := make(chan struct{})
	s := &SDKSource{
		Callbacks: p.Callbacks,
		sync: synchronizer.NewSynchronizer(func() {
			close(startRecording)
		}),
		initialized:    core.NewFuse(),
		startRecording: startRecording,
		endRecording:   make(chan struct{}),
	}

	switch p.RequestType {
	case types.RequestTypeParticipant:
		s.identity = p.Identity
	case types.RequestTypeTrackComposite:
		s.audioTrackID = p.AudioTrackID
		s.videoTrackID = p.VideoTrackID
	case types.RequestTypeTrack:
		s.trackID = p.TrackID
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
		}()
	}
	if s.videoWriter != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.videoWriter.Drain(false)
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
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackMuted: func(pub lksdk.TrackPublication, _ lksdk.Participant) {
				s.onTrackMuteChanged(pub, true)
			},
			OnTrackUnmuted: func(pub lksdk.TrackPublication, _ lksdk.Participant) {
				s.onTrackMuteChanged(pub, false)
			},
			OnTrackUnpublished: s.onTrackUnpublished,
		},
		OnDisconnected: s.onDisconnected,
	}

	if p.RequestType == types.RequestTypeParticipant {
		cb.ParticipantCallback.OnTrackPublished = s.onTrackPublished
	}

	var mu sync.Mutex
	var onSubscribeErr error
	filenameReplacements := make(map[string]string)

	cb.OnTrackSubscribed = func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		defer func() {
			s.subscriptions.Done()
			if s.initialized.IsBroken() {
				if onSubscribeErr != nil {
					s.OnFailure(onSubscribeErr)
				} else {
					s.OnTrackAdded(pub)
				}
			}
		}()

		s.active.Inc()
		t := s.sync.AddTrack(track, rp.Identity())

		mu.Lock()
		switch p.RequestType {
		case types.RequestTypeTrackComposite:
			if p.Identity == "" || track.Kind() == webrtc.RTPCodecTypeVideo {
				p.Identity = rp.Identity()
				filenameReplacements["{publisher_identity}"] = p.Identity
			}
		case types.RequestTypeTrack:
			p.Identity = rp.Identity()
			if track.Kind() == webrtc.RTPCodecTypeAudio {
				p.TrackKind = "audio"
			} else {
				p.TrackKind = "video"
				// check for video over websocket
				if p.Outputs[types.EgressTypeWebsocket] != nil {
					onSubscribeErr = errors.ErrVideoWebsocket
					return
				}
			}
			p.TrackSource = strings.ToLower(pub.Source().String())

			filenameReplacements["{track_id}"] = p.TrackID
			filenameReplacements["{track_type}"] = p.TrackKind
			filenameReplacements["{track_source}"] = p.TrackSource
			filenameReplacements["{publisher_identity}"] = p.Identity
		}
		mu.Unlock()

		var codec types.MimeType
		var appSrcName string
		var err error
		writeBlanks := false

		switch {
		case strings.EqualFold(track.Codec().MimeType, string(types.MimeTypeOpus)):
			appSrcName = AudioAppSource
			codec = types.MimeTypeOpus

			p.AudioEnabled = true
			p.AudioInCodec = codec
			if p.AudioOutCodec == "" {
				// This should only happen for track egress
				p.AudioOutCodec = codec
			}
			p.AudioTranscoding = true

			if p.RequestType == types.RequestTypeTrack {
				if o := p.GetFileConfig(); o != nil {
					o.OutputType = types.OutputTypeOGG
				}
			}

		case strings.EqualFold(track.Codec().MimeType, string(types.MimeTypeVP8)):
			appSrcName = VideoAppSource
			codec = types.MimeTypeVP8

			p.VideoEnabled = true
			p.VideoInCodec = codec
			if p.VideoOutCodec == "" {
				// This should only happen for track egress
				p.VideoOutCodec = codec
			}
			if p.VideoOutCodec != codec {
				p.VideoTranscoding = true
				writeBlanks = true
			}

			if p.RequestType == types.RequestTypeTrack {
				if o := p.GetFileConfig(); o != nil {
					o.OutputType = types.OutputTypeWebM
				}
			}

		case strings.EqualFold(track.Codec().MimeType, string(types.MimeTypeH264)):
			appSrcName = VideoAppSource
			codec = types.MimeTypeH264

			p.VideoEnabled = true
			p.VideoInCodec = codec
			if p.VideoOutCodec == "" {
				// This should only happen for track egress
				p.VideoOutCodec = types.MimeTypeH264
			}

			if p.RequestType == types.RequestTypeTrack {
				if o := p.GetFileConfig(); o != nil {
					o.OutputType = types.OutputTypeMP4
				}
			}

		default:
			onSubscribeErr = errors.ErrNotSupported(track.Codec().MimeType)
			return
		}

		<-p.GstReady
		src, err := gst.NewElementWithName("appsrc", appSrcName)
		if err != nil {
			onSubscribeErr = errors.ErrGstPipelineError(err)
			return
		}
		appSrc := app.SrcFromElement(src)

		writer, err := sdk.NewAppWriter(track, rp, codec, appSrc, s.sync, t, writeBlanks)
		if err != nil {
			logger.Errorw("could not create app writer", err)
			onSubscribeErr = err
			return
		}

		switch track.Kind() {
		case webrtc.RTPCodecTypeAudio:
			s.audioWriter = writer
			p.AudioCodecParams = track.Codec()
			p.AudioSrc = appSrc
		case webrtc.RTPCodecTypeVideo:
			s.videoWriter = writer
			p.VideoCodecParams = track.Codec()
			p.VideoSrc = appSrc
		}
	}

	s.room = lksdk.CreateRoom(cb)
	logger.Debugw("connecting to room")
	if err := s.room.JoinWithToken(p.WsUrl, p.Token, lksdk.WithAutoSubscribe(false)); err != nil {
		return err
	}

	var fileIdentifier string
	tracks := make(map[string]struct{})

	var err error
	switch p.RequestType {
	case types.RequestTypeParticipant:
		fileIdentifier = s.identity
		filenameReplacements["{publisher_identity}"] = s.identity

		s.subscriptions.Add(numTracks) // TODO: remove
		err = s.subscribeToParticipant(s.identity)

	case types.RequestTypeTrackComposite:
		fileIdentifier = p.Info.RoomName
		if p.AudioEnabled {
			s.audioTrackID = p.AudioTrackID
			tracks[s.audioTrackID] = struct{}{}
		}
		if p.VideoEnabled {
			s.videoTrackID = p.VideoTrackID
			tracks[s.videoTrackID] = struct{}{}
		}

		s.subscriptions.Add(len(tracks))
		err = s.subscribeToTracks(tracks)

	case types.RequestTypeTrack:
		fileIdentifier = p.TrackID
		s.trackID = p.TrackID
		tracks[s.trackID] = struct{}{}

		s.subscriptions.Add(1)
		err = s.subscribeToTracks(tracks)
	}
	if err != nil {
		return err
	}

	s.subscriptions.Wait()
	if onSubscribeErr != nil {
		return onSubscribeErr
	}

	if err = p.UpdateInfoFromSDK(fileIdentifier, filenameReplacements); err != nil {
		logger.Errorw("could not update file params", err)
		return err
	}

	s.initialized.Once(func() {
		logger.Infow("sdk source initialized")
	})
	return nil
}

func (s *SDKSource) subscribeToTracks(expecting map[string]struct{}) error {
	deadline := time.Now().Add(subscriptionTimeout)
	for time.Now().Before(deadline) {
		for _, p := range s.room.GetParticipants() {
			for _, track := range p.Tracks() {
				trackID := track.SID()
				if _, ok := expecting[trackID]; ok {
					if err := s.subscribe(track); err != nil {
						return err
					}

					delete(expecting, track.SID())
					if len(expecting) == 0 {
						return nil
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

func (s *SDKSource) subscribeToParticipant(identity string) error {
	deadline := time.Now().Add(subscriptionTimeout)
	for time.Now().Before(deadline) {
		for _, p := range s.room.GetParticipants() {
			if p.Identity() != identity || len(p.Tracks()) < numTracks {
				continue
			}

			for _, track := range p.Tracks() {
				switch track.Source() {
				case livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE:
					// TODO: s.subscriptions.Add(1)
					if err := s.subscribe(track); err != nil {
						return err
					}
				default:
					logger.Debugw(fmt.Sprintf("ignoring track with source %s", track.Source()))
				}
			}

			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return errors.ErrParticipantNotFound(identity)
}

func (s *SDKSource) subscribe(track lksdk.TrackPublication) error {
	logger.Infow("subscribing to track", "trackID", track.SID())
	if pub, ok := track.(*lksdk.RemoteTrackPublication); ok {
		pub.OnRTCP(s.sync.OnRTCP)

		return pub.SetSubscribed(true)
	}

	return errors.ErrInvalidTrack
}

func (s *SDKSource) onTrackMuteChanged(pub lksdk.TrackPublication, muted bool) {
	track := pub.Track()
	if track == nil {
		return
	}

	if w := s.getWriterForTrack(track.ID()); w != nil {
		w.SetTrackMuted(muted)
	}

	for _, onMute := range s.OnTrackMuted {
		onMute(muted)
	}
}

func (s *SDKSource) onTrackPublished(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	if !s.initialized.IsBroken() || rp.Identity() != s.identity {
		return
	}

	switch pub.Source() {
	case livekit.TrackSource_CAMERA:
		if s.videoWriter != nil {
			logger.Infow("ignoring participant track",
				"reason", "already recording video")
			return
		}
	case livekit.TrackSource_MICROPHONE:
		if s.audioWriter != nil {
			logger.Infow("ignoring participant track",
				"reason", "already recording video")
			return
		}
	default:
		logger.Infow("ignoring participant track",
			"reason", fmt.Sprintf("source %s", pub.Source()))
		return
	}

	s.subscriptions.Add(1)
	if err := s.subscribe(pub); err != nil {
		logger.Errorw("failed to subscribe to track", err, "trackID", pub.SID())
	}
}

// TODO: p.OnTrackRemoved
func (s *SDKSource) onTrackUnpublished(pub *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
	if w := s.getWriterForTrack(pub.SID()); w != nil {
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
