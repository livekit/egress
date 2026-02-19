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
	"github.com/linkdata/deadlock"
	"github.com/pion/webrtc/v4"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
)

const (
	subscriptionTimeout = time.Second * 30
)

type SDKSource struct {
	*config.PipelineConfig
	callbacks *gstreamer.Callbacks

	room *lksdk.Room
	sync *synchronizer.Synchronizer

	mu                   deadlock.Mutex
	initialized          core.Fuse
	filenameReplacements map[string]string

	workersMu deadlock.RWMutex
	workers   map[string]*trackWorker

	// subLock prevents a race where a subscription starts during init completion.
	// Without it, the subscription could see "not yet initialized", then init completes,
	// leaving the track orphaned (missed by both pipeline build and dynamic add).
	subLock deadlock.RWMutex

	closing atomic.Bool
	active  atomic.Int32
	closed  core.Fuse

	startRecording core.Fuse
	endRecording   core.Fuse

	timeProvider   atomic.Pointer[gstreamer.TimeProvider]
	initResultChan atomic.Pointer[chan subscriptionResult]
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
		workers:              make(map[string]*trackWorker),
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

	if p.RequestType == types.RequestTypeRoomComposite {
		// Enable Packet Burst Estimator for Room Composite requests
		opts = append(opts, synchronizer.WithStartGate())
	} else {
		// Enable Sender Report Rebase except for Room Composite
		opts = append(opts, synchronizer.WithRTCPSenderReportRebaseEnabled())
	}
	// time provider is not available yet, will be set later
	// add some leeway to the mixer latency
	opts = append(opts, synchronizer.WithMediaRunningTime(nil, p.Latency.AudioMixerLatency+200*time.Millisecond))

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
		s.disconnectRoom()
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
	s.workersMu.RLock()
	w := s.workers[trackID]
	s.workersMu.RUnlock()

	if w == nil {
		return
	}

	gen := w.generation.Load()
	s.submitOp(trackID, Operation{Type: OpPlaying, Generation: gen})
}

func (s *SDKSource) GetStartedAt() int64 {
	return s.sync.GetStartedAt()
}

func (s *SDKSource) GetEndedAt() int64 {
	return s.sync.GetEndedAt()
}

func (s *SDKSource) CloseWriters() {
	s.closed.Once(func() {
		s.closing.Store(true)
		s.sync.End()

		s.workersMu.RLock()
		workers := make([]*trackWorker, 0, len(s.workers))
		for _, w := range s.workers {
			workers = append(workers, w)
		}
		s.workersMu.RUnlock()

		for _, w := range workers {
			select {
			case w.opChan <- Operation{Type: OpClose}:
			case <-w.done.Watch():
				// already exited
			}
		}
	})
}

func (s *SDKSource) StreamStopped(elementName string) {
	trackID := strings.TrimPrefix(elementName, "app_")

	// Only send finished if we have a worker for this track
	s.workersMu.RLock()
	_, exists := s.workers[trackID]
	s.workersMu.RUnlock()

	if !exists {
		return // No worker for this track, nothing to clean up
	}

	s.submitOp(trackID, Operation{Type: OpFinished})
}

func (s *SDKSource) Close() {
	s.disconnectRoom()
}

func (s *SDKSource) SetTimeProvider(tp gstreamer.TimeProvider) {
	s.timeProvider.Store(&tp)

	if tp != nil {
		s.sync.SetMediaRunningTime(tp.RunningTime)
	} else {
		s.sync.SetMediaRunningTime(nil)
	}

	s.workersMu.RLock()
	for _, w := range s.workers {
		select {
		case w.opChan <- Operation{Type: OpSetTimeProvider, TimeProvider: tp}:
		default:
			logger.Warnw("failed to send SetTimeProvider, channel full", nil, "trackID", w.trackID)
		}
	}
	s.workersMu.RUnlock()
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

func (s *SDKSource) startAwaitingTracks(expectedCount int) <-chan subscriptionResult {
	ch := make(chan subscriptionResult, expectedCount)
	s.initResultChan.Store(&ch)
	return ch
}

// StopAwaitingTracks - called after init complete or timeout
func (s *SDKSource) stopAwaitingTracks() {
	s.initResultChan.Store(nil) // just nil out, don't close
}

func (s *SDKSource) completeInit() {
	s.subLock.Lock()
	defer s.subLock.Unlock()
	s.initialized.Break()
}

// getInitResultChan returns the current init result channel (nil after init complete)
func (s *SDKSource) getInitResultChan() chan<- subscriptionResult {
	if ptr := s.initResultChan.Load(); ptr != nil {
		return *ptr
	}
	return nil
}

// sendInitResult sends result to the init channel if non-nil (non-blocking to avoid deadlock)
func (s *SDKSource) sendInitResult(ch chan<- subscriptionResult, trackID string, err error) {
	if ch == nil {
		return
	}
	select {
	case ch <- subscriptionResult{trackID: trackID, err: err}:
	default:
		logger.Warnw("failed to send init result, channel full", nil, "trackID", trackID)
	}
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

	s.completeInit()
	return nil
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

	// get dimensions after subscribing so that track info exists
	var w, h uint32
	for _, t := range pubs {
		if t.TrackInfo().Type == livekit.TrackType_VIDEO && t.IsSubscribed() {
			w = t.TrackInfo().Width
			h = t.TrackInfo().Height
		}
	}

	s.completeInit()
	return w, h, nil
}

func (s *SDKSource) awaitExpected(expected int) error {
	if expected == 0 {
		return nil
	}

	resultChan := s.startAwaitingTracks(expected)
	defer s.stopAwaitingTracks()

	subscribed := 0
	deadline := time.After(time.Second * 3)

	for subscribed < expected {
		select {
		case sub := <-resultChan:
			if sub.err != nil {
				return sub.err
			}
			subscribed++
		case <-deadline:
			return nil
		}
	}
	return nil
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
	if trackCount == 0 {
		s.completeInit()
		return 0, 0, nil
	}

	waiting := make(map[string]struct{})
	for trackID := range expecting {
		waiting[trackID] = struct{}{}
	}

	// Set up init coordination - processIdleOp will send results here
	resultChan := s.startAwaitingTracks(trackCount)
	defer s.stopAwaitingTracks()

	deadline := time.After(subscriptionTimeout)
	tracks, err := s.subscribeToTracks(expecting, deadline)
	if err != nil {
		return 0, 0, err
	}

	for i := 0; i < trackCount; i++ {
		select {
		case result := <-resultChan:
			if result.err != nil {
				return 0, 0, result.err
			}
			delete(waiting, result.trackID)
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

	s.completeInit()
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
	// After init, only participant and room composite requests accept new tracks
	if s.shouldSkipTrackSubscriptions() {
		return
	}

	trackID := pub.SID()

	// Capture result channel at submission time (nil after init complete)
	resultChan := s.getInitResultChan()

	s.submitOp(trackID, Operation{
		Type:              OpSubscribe,
		Track:             track,
		Pub:               pub,
		RemoteParticipant: rp,
		ResultChan:        resultChan,
	})
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
	s.workersMu.RLock()
	_, exists := s.workers[pub.SID()]
	s.workersMu.RUnlock()
	if exists {
		logger.Debugw("track muted", "trackID", pub.SID())
	}
}

func (s *SDKSource) onTrackUnmuted(pub lksdk.TrackPublication, _ lksdk.Participant) {
	s.workersMu.RLock()
	_, exists := s.workers[pub.SID()]
	s.workersMu.RUnlock()
	if exists {
		logger.Debugw("track unmuted", "trackID", pub.SID())
	}
}

func (s *SDKSource) onTrackUnsubscribed(_ *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
	trackID := pub.SID()

	// Only send unsubscribe if we have a worker (i.e., we subscribed to this track)
	s.workersMu.RLock()
	_, exists := s.workers[trackID]
	s.workersMu.RUnlock()

	if !exists {
		return // Never subscribed to this track, nothing to do
	}

	logger.Debugw("track unsubscribed", "trackID", trackID)
	s.submitOp(trackID, Operation{Type: OpUnsubscribe})
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

func (s *SDKSource) shouldSkipTrackSubscriptions() bool {
	return s.initialized.IsBroken() && s.RequestType != types.RequestTypeParticipant && s.RequestType != types.RequestTypeRoomComposite
}

func (s *SDKSource) disconnectRoom() {
	if s.room != nil {
		s.room.Disconnect()
		s.room = nil
	}
}
