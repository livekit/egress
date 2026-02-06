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
	"strings"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/linkdata/deadlock"
	"github.com/pion/webrtc/v4"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/source/sdk"
	"github.com/livekit/egress/pkg/pipeline/tempo"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"
)

const (
	subscriptionTimeout = time.Second * 30
)

type trackState struct {
	writer   *sdk.AppWriter  // current active writer (nil if none)
	pending  *pendingSubData // queued subscription while cleaning
	cleaning bool            // cleanup in progress
}

type pendingSubData struct {
	track *webrtc.TrackRemote
	pub   *lksdk.RemoteTrackPublication
	rp    *lksdk.RemoteParticipant
}

type SDKSource struct {
	*config.PipelineConfig
	callbacks *gstreamer.Callbacks

	room *lksdk.Room
	sync *synchronizer.Synchronizer

	mu                   deadlock.RWMutex
	initialized          core.Fuse
	filenameReplacements map[string]string
	subs                 chan *subscriptionResult

	trackStates map[string]*trackState
	closing     bool

	subLock deadlock.RWMutex
	active  atomic.Int32
	closed  core.Fuse

	startRecording core.Fuse
	endRecording   core.Fuse

	timeProvider gstreamer.TimeProvider
}

type subscriptionResult struct {
	trackID string
	err     error
}

func NewSDKSource(ctx context.Context, p *config.PipelineConfig, callbacks *gstreamer.Callbacks) (*SDKSource, error) {
	_, span := tracer.Start(ctx, "SDKInput.New")
	defer span.End()

	s := &SDKSource{
		PipelineConfig:       p,
		callbacks:            callbacks,
		filenameReplacements: make(map[string]string),
		subs:                 make(chan *subscriptionResult, 100),
		trackStates:          make(map[string]*trackState),
		closing:              false,
	}
	logger.Debugw("latency config", "latency", p.Latency)

	opts := []synchronizer.SynchronizerOption{
		synchronizer.WithMaxTsDiff(p.Latency.RTPMaxAllowedTsDiff),
		synchronizer.WithMaxDriftAdjustment(p.Latency.RTPMaxDriftAdjustment),
		synchronizer.WithDriftAdjustmentWindowPercent(p.Latency.RTPDriftAdjustmentWindowPercent),
		synchronizer.WithOldPacketThreshold(p.Latency.OldPacketThreshold),
		synchronizer.WithOnStarted(func() {
			s.startRecording.Break()
		}),
	}

	if p.Latency.PreJitterBufferReceiveTimeEnabled {
		opts = append(opts, synchronizer.WithPreJitterBufferReceiveTimeEnabled())
	}
	if p.Latency.RTCPSenderReportRebaseEnabled {
		opts = append(opts, synchronizer.WithRTCPSenderReportRebaseEnabled())
	}
	if p.Latency.PacketBurstEstimatorEnabled {
		opts = append(opts, synchronizer.WithStartGate())
	}
	if p.Latency.EnablePipelineTimeFeedback {
		// time provider is not available yet, will be set later
		// add some leeway to the mixer latency
		opts = append(opts, synchronizer.WithMediaRunningTime(nil, p.Latency.AudioMixerLatency+200*time.Millisecond))
	}

	if p.RequestType == types.RequestTypeRoomComposite || p.AudioTempoController.Enabled {
		// in case of room composite don't adjust audio timestamps on RTCP sender reports,
		// to avoid gaps in the audio stream
		opts = append(opts, synchronizer.WithAudioPTSAdjustmentDisabled())
		if p.AudioTempoController.Enabled {
			logger.Debugw("audio tempo controller enabled", "adjustmentRate", p.AudioTempoController.AdjustmentRate)
		}
	}

	s.sync = synchronizer.NewSynchronizerWithOptions(
		opts...,
	)

	if err := s.joinRoom(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *SDKSource) StartRecording() <-chan struct{} {
	return s.startRecording.Watch()
}

func (s *SDKSource) EndRecording() <-chan struct{} {
	return s.endRecording.Watch()
}

func (s *SDKSource) Playing(trackID string) {
	s.mu.RLock()
	state := s.trackStates[trackID]
	var writer *sdk.AppWriter
	if state != nil {
		writer = state.writer
	}
	s.mu.RUnlock()

	if writer != nil {
		writer.Playing()
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

		s.mu.Lock()
		s.closing = true

		var writers []*sdk.AppWriter
		for _, state := range s.trackStates {
			if state.writer != nil {
				writers = append(writers, state.writer)
				state.cleaning = true
			}
			state.pending = nil
		}

		s.mu.Unlock()

		for _, writer := range writers {
			go writer.Drain(false)
		}
	})
}

func (s *SDKSource) StreamStopped(elementName string) {
	trackID := strings.TrimPrefix(elementName, "app_")

	s.mu.Lock()
	state := s.trackStates[trackID]
	if state == nil || state.writer == nil {
		s.mu.Unlock()
		return
	}

	if state.cleaning {
		logger.Debugw("cleanup already in progress, clearing pending", "trackID", trackID)
		state.pending = nil
		s.mu.Unlock()
		return
	}

	state.cleaning = true
	state.pending = nil
	writer := state.writer
	s.mu.Unlock()

	s.onTrackFinished(trackID, writer)
}

func (s *SDKSource) Close() {
	s.room.Disconnect()
}

func (s *SDKSource) SetTimeProvider(tp gstreamer.TimeProvider) {
	s.mu.Lock()
	s.timeProvider = tp
	if s.Latency.EnablePipelineTimeFeedback && tp != nil {
		s.sync.SetMediaRunningTime(tp.RunningTime)
	} else {
		s.sync.SetMediaRunningTime(nil)
	}
	for _, trackState := range s.trackStates {
		w := trackState.writer
		if w == nil {
			continue
		}
		w.SetTimeProvider(tp)
	}
	s.mu.Unlock()
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
		OnDisconnected: s.onDisconnected,
	}

	if s.RequestType == types.RequestTypeRoomComposite {
		cb.ParticipantCallback.OnTrackPublished = s.onTrackPublished
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
	case types.RequestTypeRoomComposite:
		fileIdentifier = s.room.Name()
		// room_name and room_id are already handled as replacements

		err = s.awaitRoomTracks()

	case types.RequestTypeParticipant:
		fileIdentifier = s.Identity
		s.filenameReplacements["{publisher_identity}"] = s.Identity
		w, h, err = s.awaitParticipantTracks(s.Identity)

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

func (s *SDKSource) awaitRoomTracks() error {
	// await expected subscriptions
	expected := 0
	for _, rp := range s.room.GetRemoteParticipants() {
		pubs := rp.TrackPublications()
		for _, pub := range pubs {
			if s.shouldSubscribe(pub) {
				expected++
			}
		}
	}
	if err := s.awaitExpected(expected); err != nil {
		return err
	}

	// lock any incoming subscriptions
	s.subLock.Lock()
	defer s.subLock.Unlock()

	for {
		select {
		// check errors from any tracks published in the meantime
		case sub := <-s.subs:
			if sub.err != nil {
				return sub.err
			}
		default:
			// ready
			s.initialized.Break()
			return nil
		}
	}
}

func (s *SDKSource) awaitParticipantTracks(identity string) (uint32, uint32, error) {
	rp, err := s.getParticipant(identity)
	if err != nil {
		return 0, 0, err
	}

	// await expected subscriptions
	pubs := rp.TrackPublications()
	expected := 0
	for _, pub := range pubs {
		if s.shouldSubscribe(pub) {
			expected++
		}
	}
	if err = s.awaitExpected(expected); err != nil {
		return 0, 0, err
	}

	// lock any incoming subscriptions
	s.subLock.Lock()
	defer s.subLock.Unlock()

	for {
		select {
		// check errors from any tracks published in the meantime
		case sub := <-s.subs:
			if sub.err != nil {
				return 0, 0, sub.err
			}
		default:
			// get dimensions after subscribing so that track info exists
			var w, h uint32
			for _, t := range pubs {
				if t.TrackInfo().Type == livekit.TrackType_VIDEO && t.IsSubscribed() {
					w = t.TrackInfo().Width
					h = t.TrackInfo().Height
				}
			}

			// ready
			s.initialized.Break()
			return w, h, nil
		}
	}
}

func (s *SDKSource) awaitExpected(expected int) error {
	subscribed := 0
	deadline := make(chan struct{})
	time.AfterFunc(time.Second*3, func() {
		close(deadline)
	})

	for {
		select {
		case sub := <-s.subs:
			if sub.err != nil {
				return sub.err
			}
			subscribed++
			if subscribed == expected {
				return nil
			}
		case <-deadline:
			return nil
		}
	}
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
	waiting := make(map[string]struct{})
	for trackID := range expecting {
		waiting[trackID] = struct{}{}
	}

	deadline := time.After(subscriptionTimeout)
	tracks, err := s.subscribeToTracks(expecting, deadline)
	if err != nil {
		return 0, 0, err
	}

	for i := 0; i < trackCount; i++ {
		select {
		case sub := <-s.subs:
			if sub.err != nil {
				return 0, 0, sub.err
			}
			delete(waiting, sub.trackID)
		case <-deadline:
			for trackID := range waiting {
				return 0, 0, errors.ErrTrackNotFound(trackID)
			}
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
						if trackID == s.AudioTrackID && track.Kind() == lksdk.TrackKindVideo {
							return nil, errors.ErrInvalidInput("audio_track_id")
						} else if trackID == s.VideoTrackID && track.Kind() == lksdk.TrackKindAudio {
							return nil, errors.ErrInvalidInput("video_track_id")
						}

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
	trackID := pub.SID()

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		logger.Debugw("ignoring subscription, source closing", "trackID", trackID)
		return
	}

	state := s.trackStates[trackID]
	if state != nil && state.cleaning {
		state.pending = &pendingSubData{track: track, pub: pub, rp: rp}
		s.mu.Unlock()
		logger.Debugw("subscription queued, cleanup in progress", "trackID", trackID)
		return
	}

	if state == nil {
		state = &trackState{}
		s.trackStates[trackID] = state
	}
	s.mu.Unlock()

	s.subLock.RLock()

	if s.initialized.IsBroken() && s.RequestType != types.RequestTypeParticipant && s.RequestType != types.RequestTypeRoomComposite {
		s.subLock.RUnlock()
		return
	}

	var onSubscribeErr error
	var subscriptionQueued bool
	defer func() {
		if subscriptionQueued {
			// Subscription was queued as pending - result will be sent when pending executes
			s.subLock.RUnlock()
			return
		}
		if s.initialized.IsBroken() {
			if onSubscribeErr != nil {
				s.callbacks.OnError(onSubscribeErr)
			}
		} else {
			s.subs <- &subscriptionResult{
				trackID: pub.SID(),
				err:     onSubscribeErr,
			}
		}
		s.subLock.RUnlock()
	}()

	ts := &config.TrackSource{
		TrackID:         pub.SID(),
		TrackKind:       pub.Kind(),
		ParticipantKind: rp.Kind(),
		MimeType:        types.MimeType(strings.ToLower(track.Codec().MimeType)),
		PayloadType:     track.Codec().PayloadType,
		ClockRate:       track.Codec().ClockRate,
	}

	<-s.callbacks.GstReady
	switch ts.MimeType {
	case types.MimeTypeOpus, types.MimeTypePCMU, types.MimeTypePCMA:
		s.AudioEnabled = true
		s.AudioInCodec = ts.MimeType
		if s.AudioOutCodec == "" {
			// PCMU/PCMA are input-only codecs, use Opus as default output
			if ts.MimeType == types.MimeTypePCMU || ts.MimeType == types.MimeTypePCMA {
				s.AudioOutCodec = types.MimeTypeOpus
			} else {
				s.AudioOutCodec = ts.MimeType
			}
		}
		s.AudioTranscoding = true

		var tc sdk.DriftHandler
		if s.AudioTempoController.Enabled {
			c := tempo.NewController()
			ts.TempoController = c
			tc = c
		}
		writer, err := s.createWriter(track, pub, rp, ts, tc)
		if err != nil {
			onSubscribeErr = err
			return
		}
		if writer == nil {
			s.tryQueuePendingSubscription(ts.TrackID, track, pub, rp)
			subscriptionQueued = true
			return
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

		writer, err := s.createWriter(track, pub, rp, ts, nil)
		if err != nil {
			onSubscribeErr = err
			return
		}
		if writer == nil {
			s.tryQueuePendingSubscription(ts.TrackID, track, pub, rp)
			subscriptionQueued = true
			return
		}

	default:
		onSubscribeErr = errors.ErrNotSupported(string(ts.MimeType))
		return
	}

	if s.initialized.IsBroken() {
		<-s.callbacks.BuildReady
		s.callbacks.OnTrackAdded(ts)
	} else {
		s.mu.Lock()
		switch s.RequestType {
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
	tc sdk.DriftHandler,
) (*sdk.AppWriter, error) {
	src, err := gst.NewElementWithName("appsrc", fmt.Sprintf("app_%s", track.ID()))
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	ts.AppSrc = app.SrcFromElement(src)

	writer, err := sdk.NewAppWriter(s.PipelineConfig, track, pub, rp, ts, s.sync, tc, s.callbacks)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	writer.SetTimeProvider(s.timeProvider)
	s.mu.RUnlock()

	// Store the writer - returns nil if state changed during creation
	if !s.storeWriter(ts.TrackID, writer, ts) {
		return nil, nil
	}

	return writer, nil
}

// tryQueuePendingSubscription attempts to queue a subscription that failed to store.
// Called when createWriter returns nil (state changed during creation).
// If state.cleaning is true, queues the subscription for later execution.
func (s *SDKSource) tryQueuePendingSubscription(trackID string, track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.trackStates[trackID]
	if state != nil && state.cleaning && !s.closing {
		state.pending = &pendingSubData{track: track, pub: pub, rp: rp}
		logger.Debugw("subscription queued after failed store", "trackID", trackID)
	}
}

func (s *SDKSource) storeWriter(trackID string, writer *sdk.AppWriter, ts *config.TrackSource) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.trackStates[trackID]
	if state == nil || state.cleaning || s.closing {
		logger.Warnw("state changed during writer creation", nil, "trackID", trackID)
		go s.handleOrphanedWriter(trackID, writer)
		return false
	}
	state.writer = writer
	s.active.Inc()

	if s.timeProvider != nil {
		writer.SetTimeProvider(s.timeProvider)
	}
	if !s.initialized.IsBroken() {
		if ts.TrackKind == lksdk.TrackKindAudio {
			s.AudioTracks = append(s.AudioTracks, ts)
		} else {
			s.VideoTrack = ts
		}
	}
	return true
}

func (s *SDKSource) onTrackPublished(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	if s.RequestType != types.RequestTypeParticipant && s.RequestType != types.RequestTypeRoomComposite {
		return
	}

	if s.RequestType == types.RequestTypeParticipant && rp.Identity() != s.Identity {
		return
	}

	if s.shouldSubscribe(pub) {
		if err := s.subscribe(pub); err != nil {
			logger.Errorw("failed to subscribe to track", err, "trackID", pub.SID())
		}
	} else {
		logger.Infow("ignoring track", "reason", fmt.Sprintf("source %s", pub.Source()))
	}
}

func (s *SDKSource) shouldSubscribe(pub lksdk.TrackPublication) bool {
	switch s.RequestType {
	case types.RequestTypeParticipant:
		switch pub.Source() {
		case livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE:
			return !s.ScreenShare
		default:
			return s.ScreenShare
		}
	case types.RequestTypeRoomComposite:
		switch pub.Kind() {
		case lksdk.TrackKindAudio:
			return s.AudioEnabled
		case lksdk.TrackKindVideo:
			return s.VideoEnabled
		}
	}

	return false
}

func (s *SDKSource) onTrackMuted(pub lksdk.TrackPublication, _ lksdk.Participant) {
	s.mu.RLock()
	state := s.trackStates[pub.SID()]
	s.mu.RUnlock()
	if state != nil && state.writer != nil {
		logger.Debugw("track muted", "trackID", pub.SID())
	}
}

func (s *SDKSource) onTrackUnmuted(pub lksdk.TrackPublication, _ lksdk.Participant) {
	s.mu.RLock()
	state := s.trackStates[pub.SID()]
	s.mu.RUnlock()
	if state != nil && state.writer != nil {
		logger.Debugw("track unmuted", "trackID", pub.SID())
	}
}

func (s *SDKSource) onTrackUnsubscribed(_ *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
	trackID := pub.SID()
	logger.Debugw("track unsubscribed", "trackID", trackID)

	s.mu.Lock()
	if s.closing {
		logger.Debugw("ignoring track unsubscribed, source closing", "trackID", trackID)
		s.mu.Unlock()
		return
	}
	state := s.trackStates[trackID]
	if state == nil || state.writer == nil {
		logger.Warnw("ignoring track unsubscribed, writer not found", nil, "trackID", trackID)
		s.mu.Unlock()
		return
	}

	if state.cleaning {
		logger.Debugw("ignoring track unsubscribed, cleanup in progress", "trackID", trackID)
		state.pending = nil
		s.mu.Unlock()
		return
	}

	state.cleaning = true
	writer := state.writer
	s.mu.Unlock()

	// Signal unsubscribed but let the reader continue until error or grace period.
	// This allows any remaining buffers in flight from the SFU to be processed.
	writer.OnUnsubscribed()

	go func() {
		logger.Debugw("waiting for writer to finish", "trackID", trackID)
		// Wait for the writer to finish naturally
		<-writer.Finished()
		logger.Debugw("writer finished", "trackID", trackID)
		s.onTrackFinished(trackID, writer)
		logger.Debugw("track finished", "trackID", trackID)
	}()
}

func (s *SDKSource) onTrackFinished(trackID string, finishedWriter *sdk.AppWriter) {
	s.mu.Lock()
	state := s.trackStates[trackID]
	if state == nil || state.writer != finishedWriter || finishedWriter == nil || !state.cleaning {
		s.mu.Unlock()
		logger.Warnw("ignoring track finished, stale writer or writer is nil or not cleaning", nil,
			"trackID", trackID,
			"finishedWriter", finishedWriter,
		)
		return
	}

	// Clear writer but keep cleaning=true during cleanup
	// This ensures new subscriptions queue in pending instead of creating writers
	state.writer = nil
	s.mu.Unlock()

	active := s.active.Dec()
	shouldContinue := s.RequestType == types.RequestTypeParticipant || s.RequestType == types.RequestTypeRoomComposite

	if shouldContinue {
		trackKind := finishedWriter.TrackKind()
		if trackKind == webrtc.RTPCodecTypeAudio {
			// drain for video tracks could set EOS for encoder which could lead to issues after switching
			// to test videosrc if participant stays, so drain audio tracks at this point and only do that after
			// removing the appsource for video tracks
			finishedWriter.Drain(true)
		}
		s.sync.RemoveTrack(trackID)
		<-s.callbacks.BuildReady
		s.callbacks.OnTrackRemoved(trackID)

		if trackKind == webrtc.RTPCodecTypeVideo {
			finishedWriter.Drain(true)
		}
	} else {
		finishedWriter.Drain(true)
		if active == 0 {
			s.finished()
		}
	}

	s.mu.Lock()
	state = s.trackStates[trackID]
	if state == nil {
		logger.Debugw("state changed during track finished, no need to execute pending subscription", "trackID", trackID)
		s.mu.Unlock()
		return
	}
	// After this unlock, new subscriptions create writers directly (won't queue)
	pending := state.pending
	state.cleaning = false
	state.pending = nil
	s.mu.Unlock()

	if pending != nil && !s.closing {
		logger.Debugw("executing pending subscription", "trackID", trackID)
		s.onTrackSubscribed(pending.track, pending.pub, pending.rp)
	}

}

func (s *SDKSource) onParticipantDisconnected(rp *lksdk.RemoteParticipant) {
	if rp.Identity() == s.Identity {
		logger.Debugw("participant disconnected")
		s.finished()
	}
}

func (s *SDKSource) onDisconnected() {
	logger.Warnw("disconnected from room", nil)
	s.finished()
}

func (s *SDKSource) finished() {
	s.endRecording.Break()
}

func (s *SDKSource) handleOrphanedWriter(trackID string, writer *sdk.AppWriter) {
	writer.Drain(true)
	logger.Debugw("orphaned writer cleaned up", "trackID", trackID)
}
