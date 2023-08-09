// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package source

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/source/sdk"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/livekit/server-sdk-go/pkg/synchronizer"
)

const (
	subscriptionTimeout = time.Second * 30
)

type SDKSource struct {
	*config.PipelineConfig

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
		PipelineConfig: p,
		sync: synchronizer.NewSynchronizer(func() {
			close(startRecording)
		}),
		startRecording: startRecording,
		endRecording:   make(chan struct{}),
	}

	switch p.RequestType {
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

func (s *SDKSource) Playing(trackID string) {
	if w := s.getWriterForTrack(trackID); w != nil {
		w.Play()
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

func (s *SDKSource) StreamStopped(trackID string) {
	s.onTrackFinished(trackID)
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

	var mu sync.Mutex
	var onSubscribeErr error
	filenameReplacements := make(map[string]string)

	cb.OnTrackSubscribed = func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		defer s.subscriptions.Done()

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
		var writeBlanks bool
		var err error

		switch {
		case strings.EqualFold(track.Codec().MimeType, string(types.MimeTypeOpus)):
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

		var logFilename string
		if p.Debug.EnableProfiling {
			if p.Debug.ToUploadConfig() == nil {
				logFilename = path.Join(p.Debug.PathPrefix, fmt.Sprintf("%s.csv", track.ID()))
			} else {
				logFilename = path.Join(p.TmpDir, fmt.Sprintf("%s.csv", track.ID()))
			}
		}

		<-p.GstReady
		src, err := gst.NewElementWithName("appsrc", track.ID())
		if err != nil {
			onSubscribeErr = errors.ErrGstPipelineError(err)
			return
		}

		appSrc := app.SrcFromElement(src)
		writer, err := sdk.NewAppWriter(track, rp, codec, appSrc, s.sync, t, writeBlanks, logFilename)
		if err != nil {
			logger.Errorw("could not create app writer", err)
			onSubscribeErr = err
			return
		}

		ts := &config.TrackSource{
			TrackID: pub.SID(),
			Kind:    pub.Kind(),
			AppSrc:  appSrc,
			Codec:   track.Codec(),
		}

		switch track.Kind() {
		case webrtc.RTPCodecTypeAudio:
			s.audioWriter = writer
			p.AudioTrack = ts
		case webrtc.RTPCodecTypeVideo:
			s.videoWriter = writer
			p.VideoTrack = ts
		}
	}

	s.room = lksdk.CreateRoom(cb)
	logger.Debugw("connecting to room")
	if err := s.room.JoinWithToken(p.WsUrl, p.Token, lksdk.WithAutoSubscribe(false)); err != nil {
		return err
	}

	var fileIdentifier string
	tracks := make(map[string]struct{})

	switch p.RequestType {
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

	case types.RequestTypeTrack:
		fileIdentifier = p.TrackID
		s.trackID = p.TrackID
		tracks[s.trackID] = struct{}{}
	}

	s.subscriptions.Add(len(tracks))
	if err := s.subscribeToTracks(tracks); err != nil {
		return err
	}
	s.subscriptions.Wait()
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

func (s *SDKSource) subscribe(track lksdk.TrackPublication) error {
	if pub, ok := track.(*lksdk.RemoteTrackPublication); ok {
		if pub.IsSubscribed() {
			s.subscriptions.Done()
			return nil
		}

		s.active.Inc()
		logger.Infow("subscribing to track", "trackID", track.SID())

		pub.OnRTCP(s.sync.OnRTCP)
		return pub.SetSubscribed(true)
	}

	return errors.ErrInvalidTrack
}

func (s *SDKSource) onTrackMuteChanged(pub lksdk.TrackPublication, muted bool) {
	if w := s.getWriterForTrack(pub.SID()); w != nil {
		w.SetTrackMuted(muted)
		for _, onMute := range s.OnTrackMuted {
			onMute(muted)
		}
	}
}

func (s *SDKSource) onTrackUnpublished(pub *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
	if w := s.getWriterForTrack(pub.SID()); w != nil {
		w.Drain(true)
		if s.active.Dec() == 0 {
			s.onDisconnected()
		}
	}
}

func (s *SDKSource) onTrackFinished(trackID string) {
	var w *sdk.AppWriter

	if s.audioWriter != nil && s.audioWriter.TrackID() == trackID {
		logger.Infow("removing audio writer")
		w = s.audioWriter
		s.audioWriter = nil
	} else if s.videoWriter != nil && s.videoWriter.TrackID() == trackID {
		logger.Infow("removing video writer")
		w = s.videoWriter
		s.videoWriter = nil
	} else {
		return
	}

	w.Drain(true)
	if s.active.Dec() == 0 {
		s.onDisconnected()
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
	if s.audioWriter != nil && s.audioWriter.TrackID() == trackID {
		return s.audioWriter
	}
	if s.videoWriter != nil && s.videoWriter.TrackID() == trackID {
		return s.videoWriter
	}
	return nil
}
