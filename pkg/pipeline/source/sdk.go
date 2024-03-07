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

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/webrtc/v3"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/source/sdk"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"
)

const (
	subscriptionTimeout = time.Second * 30
)

type SDKSource struct {
	*config.PipelineConfig
	callbacks *gstreamer.Callbacks

	room *lksdk.Room
	sync *synchronizer.Synchronizer

	mu                   sync.RWMutex
	initialized          core.Fuse
	filenameReplacements map[string]string
	errors               chan error

	writers map[string]*sdk.AppWriter
	active  atomic.Int32
	closed  core.Fuse

	startRecording chan struct{}
	endRecording   chan struct{}
}

func NewSDKSource(ctx context.Context, p *config.PipelineConfig, callbacks *gstreamer.Callbacks) (*SDKSource, error) {
	ctx, span := tracer.Start(ctx, "SDKInput.New")
	defer span.End()

	startRecording := make(chan struct{})
	s := &SDKSource{
		PipelineConfig: p,
		callbacks:      callbacks,
		sync: synchronizer.NewSynchronizer(func() {
			close(startRecording)
		}),
		filenameReplacements: make(map[string]string),
		writers:              make(map[string]*sdk.AppWriter),
		startRecording:       startRecording,
		endRecording:         make(chan struct{}),
	}

	if err := s.joinRoom(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *SDKSource) StartRecording() chan struct{} {
	return s.startRecording
}

func (s *SDKSource) EndRecording() chan struct{} {
	return s.endRecording
}

func (s *SDKSource) Playing(trackID string) {
	s.mu.Lock()
	writer := s.writers[trackID]
	s.mu.Unlock()

	if writer != nil {
		writer.Play()
	}
}

func (s *SDKSource) GetStartedAt() int64 {
	return s.sync.GetStartedAt()
}

func (s *SDKSource) GetEndedAt() int64 {
	return s.sync.GetEndedAt()
}

func (s *SDKSource) CloseWriters() {
	s.closed.Once(func() {
		s.sync.End()

		var wg sync.WaitGroup
		s.mu.Lock()
		wg.Add(len(s.writers))
		for _, w := range s.writers {
			go func(writer *sdk.AppWriter) {
				defer wg.Done()
				writer.Drain(false)
			}(w)
		}
		s.mu.Unlock()
		wg.Wait()
	})
}

func (s *SDKSource) StreamStopped(trackID string) {
	s.onTrackFinished(trackID)
}

func (s *SDKSource) Close() {
	s.room.Disconnect()
}

// ----- Subscriptions -----

func (s *SDKSource) joinRoom() error {
	cb := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed:   s.onTrackSubscribed,
			OnTrackMuted:        s.onTrackMuted,
			OnTrackUnmuted:      s.onTrackUnmuted,
			OnTrackUnsubscribed: s.onTrackUnsubscribed,
		},
		OnReconnecting: s.onReconnecting,
		OnReconnected:  s.onReconnected,
		OnDisconnected: s.onDisconnected,
	}
	if s.RequestType == types.RequestTypeParticipant {
		cb.ParticipantCallback.OnTrackPublished = s.onTrackPublished
		cb.OnParticipantDisconnected = s.onParticipantDisconnected
	}

	logger.Debugw("connecting to room")
	room, err := lksdk.ConnectToRoomWithToken(s.WsUrl, s.Token, cb, lksdk.WithAutoSubscribe(false))
	if err != nil {
		return err
	}
	s.room = room

	var fileIdentifier string
	var w, h uint32
	switch s.RequestType {
	case types.RequestTypeParticipant:
		fileIdentifier = s.Identity
		w, h, err = s.awaitParticipant(s.Identity)

	case types.RequestTypeTrackComposite:
		fileIdentifier = s.Info.RoomName
		tracks := make(map[string]struct{})
		if s.AudioEnabled {
			tracks[s.AudioTrackID] = struct{}{}
		}
		if s.VideoEnabled {
			tracks[s.VideoTrackID] = struct{}{}
		}
		w, h, err = s.awaitTracks(tracks)

	case types.RequestTypeTrack:
		fileIdentifier = s.TrackID
		w, h, err = s.awaitTracks(map[string]struct{}{s.TrackID: {}})
	}
	if err != nil {
		return err
	}

	if err = s.UpdateInfoFromSDK(fileIdentifier, s.filenameReplacements, w, h); err != nil {
		logger.Errorw("could not update file params", err)
		return err
	}

	return nil
}

func (s *SDKSource) awaitParticipant(identity string) (uint32, uint32, error) {
	s.errors = make(chan error, 2)

	rp, err := s.getParticipant(identity)
	if err != nil {
		return 0, 0, err
	}

	for trackCount := 0; trackCount == 0 || trackCount < len(rp.TrackPublications()); trackCount++ {
		if err = <-s.errors; err != nil {
			return 0, 0, err
		}
	}

	var w, h uint32
	for _, t := range rp.TrackPublications() {
		if t.TrackInfo().Type == livekit.TrackType_VIDEO {
			w = t.TrackInfo().Width
			h = t.TrackInfo().Height
		}

	}

	s.initialized.Break()
	return w, h, nil
}

func (s *SDKSource) getParticipant(identity string) (*lksdk.RemoteParticipant, error) {
	deadline := time.Now().Add(subscriptionTimeout)
	for time.Now().Before(deadline) {
		for _, p := range s.room.GetRemoteParticipants() {
			if p.Identity() == identity {
				return p, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, errors.ErrParticipantNotFound(identity)
}

func (s *SDKSource) awaitTracks(expecting map[string]struct{}) (uint32, uint32, error) {
	trackCount := len(expecting)
	s.errors = make(chan error, trackCount)

	deadline := time.After(subscriptionTimeout)
	tracks, err := s.subscribeToTracks(expecting, deadline)
	if err != nil {
		return 0, 0, err
	}

	for i := 0; i < trackCount; i++ {
		select {
		case err := <-s.errors:
			if err != nil {
				return 0, 0, err
			}
		case <-deadline:
			return 0, 0, errors.ErrSubscriptionFailed
		}
	}

	var w, h uint32
	for _, t := range tracks {
		if t.TrackInfo().Type == livekit.TrackType_VIDEO {
			w = t.TrackInfo().Width
			h = t.TrackInfo().Height
		}
	}

	s.initialized.Break()
	return w, h, nil
}

func (s *SDKSource) subscribeToTracks(expecting map[string]struct{}, deadline <-chan time.Time) ([]lksdk.TrackPublication, error) {
	var tracks []lksdk.TrackPublication

	for {
		select {
		case <-deadline:
			for trackID := range expecting {
				return nil, errors.ErrTrackNotFound(trackID)
			}
		default:
			for _, p := range s.room.GetRemoteParticipants() {
				for _, track := range p.TrackPublications() {
					trackID := track.SID()
					if _, ok := expecting[trackID]; ok {
						if err := s.subscribe(track); err != nil {
							return nil, err
						}

						tracks = append(tracks, track)

						delete(expecting, track.SID())
						if len(expecting) == 0 {
							return tracks, nil
						}
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func (s *SDKSource) subscribe(track lksdk.TrackPublication) error {
	if pub, ok := track.(*lksdk.RemoteTrackPublication); ok {
		if pub.IsSubscribed() {
			return nil
		}

		logger.Infow("subscribing to track", "trackID", track.SID())

		pub.OnRTCP(s.sync.OnRTCP)
		return pub.SetSubscribed(true)
	}

	return errors.ErrSubscriptionFailed
}

// ----- Callbacks -----

func (s *SDKSource) onTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	if s.initialized.IsBroken() && s.RequestType != types.RequestTypeParticipant {
		return
	}

	var onSubscribeErr error
	defer func() {
		if s.initialized.IsBroken() {
			if onSubscribeErr != nil {
				s.callbacks.OnError(onSubscribeErr)
			}
		} else {
			s.errors <- onSubscribeErr
		}
	}()

	s.active.Inc()
	ts := &config.TrackSource{
		TrackID:     pub.SID(),
		Kind:        pub.Kind(),
		MimeType:    types.MimeType(strings.ToLower(track.Codec().MimeType)),
		PayloadType: track.Codec().PayloadType,
		ClockRate:   track.Codec().ClockRate,
	}

	<-s.callbacks.GstReady
	switch ts.MimeType {
	case types.MimeTypeOpus:
		s.AudioEnabled = true
		s.AudioInCodec = ts.MimeType
		if s.AudioOutCodec == "" {
			s.AudioOutCodec = ts.MimeType
		}
		s.AudioTranscoding = true

		writer, err := s.createWriter(track, pub, rp, ts)
		if err != nil {
			onSubscribeErr = err
			return
		}

		s.mu.Lock()
		s.writers[ts.TrackID] = writer
		s.mu.Unlock()

		if s.initialized.IsBroken() {
			s.callbacks.OnTrackAdded(ts)
		} else {
			s.AudioTrack = ts
		}

	case types.MimeTypeH264, types.MimeTypeVP8, types.MimeTypeVP9:
		s.VideoEnabled = true
		s.VideoInCodec = ts.MimeType
		if s.VideoOutCodec == "" {
			s.VideoOutCodec = ts.MimeType
		}
		if s.VideoInCodec != s.VideoOutCodec {
			s.VideoDecoding = true
			if len(s.GetEncodedOutputs()) > 0 {
				s.VideoEncoding = true
			}
		}

		writer, err := s.createWriter(track, pub, rp, ts)
		if err != nil {
			onSubscribeErr = err
			return
		}

		s.mu.Lock()
		s.writers[ts.TrackID] = writer
		s.mu.Unlock()

		if s.initialized.IsBroken() {
			s.callbacks.OnTrackAdded(ts)
		} else {
			s.VideoTrack = ts
		}

	default:
		onSubscribeErr = errors.ErrNotSupported(string(ts.MimeType))
		return
	}

	if !s.initialized.IsBroken() {
		s.mu.Lock()
		switch s.RequestType {
		case types.RequestTypeParticipant:
			s.filenameReplacements["{publisher_identity}"] = s.Identity

		case types.RequestTypeTrackComposite:
			if s.Identity == "" || track.Kind() == webrtc.RTPCodecTypeVideo {
				s.Identity = rp.Identity()
				s.filenameReplacements["{publisher_identity}"] = s.Identity
			}

		case types.RequestTypeTrack:
			s.Identity = rp.Identity()
			s.TrackKind = pub.Kind().String()
			if pub.Kind() == lksdk.TrackKindVideo && s.Outputs[types.EgressTypeWebsocket] != nil {
				onSubscribeErr = errors.ErrIncompatible("websocket", ts.MimeType)
				s.mu.Unlock()
				return
			}
			s.TrackSource = strings.ToLower(pub.Source().String())
			if o := s.GetFileConfig(); o != nil {
				o.OutputType = types.TrackOutputTypes[ts.MimeType]
			}

			s.filenameReplacements["{track_id}"] = s.TrackID
			s.filenameReplacements["{track_type}"] = s.TrackKind
			s.filenameReplacements["{track_source}"] = s.TrackSource
			s.filenameReplacements["{publisher_identity}"] = s.Identity
		}
		s.mu.Unlock()
	}
}

func (s *SDKSource) createWriter(
	track *webrtc.TrackRemote,
	pub lksdk.TrackPublication,
	rp *lksdk.RemoteParticipant,
	ts *config.TrackSource,
) (*sdk.AppWriter, error) {
	var logFilename string
	if s.Debug.EnableProfiling {
		if s.Debug.ToUploadConfig() == nil {
			logFilename = path.Join(s.Debug.PathPrefix, fmt.Sprintf("%s.csv", track.ID()))
		} else {
			logFilename = path.Join(s.TmpDir, fmt.Sprintf("%s.csv", track.ID()))
		}
	}

	src, err := gst.NewElementWithName("appsrc", fmt.Sprintf("app_%s", track.ID()))
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	ts.AppSrc = app.SrcFromElement(src)
	writer, err := sdk.NewAppWriter(track, pub, rp, ts, s.sync, s.callbacks, logFilename)
	if err != nil {
		return nil, err
	}

	return writer, nil
}

func (s *SDKSource) onTrackPublished(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	if rp.Identity() != s.Identity {
		return
	}

	switch pub.Source() {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE:
		if err := s.subscribe(pub); err != nil {
			logger.Errorw("failed to subscribe to track", err, "trackID", pub.SID())
		}
	default:
		logger.Infow("ignoring participant track",
			"reason", fmt.Sprintf("source %s", pub.Source()))
		return
	}
}

func (s *SDKSource) onTrackMuted(pub lksdk.TrackPublication, _ lksdk.Participant) {
	s.mu.Lock()
	writer := s.writers[pub.SID()]
	s.mu.Unlock()

	if writer != nil {
		writer.SetTrackMuted(true)
	}
}

func (s *SDKSource) onTrackUnmuted(pub lksdk.TrackPublication, _ lksdk.Participant) {
	s.mu.Lock()
	writer := s.writers[pub.SID()]
	s.mu.Unlock()

	if writer != nil {
		writer.SetTrackMuted(false)
	}
}

func (s *SDKSource) onTrackUnsubscribed(_ *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
	logger.Debugw("track unsubscribed", "trackID", pub.SID())
	s.onTrackFinished(pub.SID())
}

func (s *SDKSource) onTrackFinished(trackID string) {
	s.mu.Lock()
	writer := s.writers[trackID]
	delete(s.writers, trackID)
	s.mu.Unlock()

	if writer != nil {
		writer.Drain(true)
		active := s.active.Dec()
		if s.RequestType == types.RequestTypeParticipant {
			s.callbacks.OnTrackRemoved(trackID)
			s.sync.RemoveTrack(trackID)
		} else if active == 0 {
			s.finished()
		}
	}
}

func (s *SDKSource) onParticipantDisconnected(rp *lksdk.RemoteParticipant) {
	if rp.Identity() == s.Identity {
		logger.Debugw("participant disconnected")
		s.finished()
	}
}

func (s *SDKSource) onReconnecting() {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, writer := range s.writers {
		writer.SetTrackDisconnected(true)
	}
}

func (s *SDKSource) onReconnected() {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, writer := range s.writers {
		writer.SetTrackDisconnected(false)
	}
}

func (s *SDKSource) onDisconnected() {
	logger.Warnw("disconnected from room", nil)
	s.finished()
}

func (s *SDKSource) finished() {
	select {
	case <-s.endRecording:
		return
	default:
		close(s.endRecording)
	}
}
