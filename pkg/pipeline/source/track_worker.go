// Copyright 2026 LiveKit, Inc.
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
	"fmt"
	"strings"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/webrtc/v4"
	"go.uber.org/atomic"

	"github.com/frostbyte73/core"
	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/source/sdk"
	"github.com/livekit/egress/pkg/pipeline/tempo"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// TrackState represents the state of a track writer in the per-track worker state machine.
type TrackState int

const (
	TrackStateIdle     TrackState = iota // no active writer, ready for subscription
	TrackStateActive                     // writer is active and processing samples
	TrackStateCleaning                   // writer is draining after unsubscribe
)

func (s TrackState) String() string {
	switch s {
	case TrackStateIdle:
		return "IDLE"
	case TrackStateActive:
		return "ACTIVE"
	case TrackStateCleaning:
		return "CLEANING"
	default:
		return "UNKNOWN"
	}
}

// OpType represents operations that can be sent to a track worker.
type OpType int

const (
	OpSubscribe       OpType = iota // track subscribed, create writer
	OpUnsubscribe                   // track unsubscribed, start graceful cleanup
	OpFinished                      // StreamStopped, immediate cleanup
	OpPlaying                       // GStreamer pipeline is playing
	OpSetTimeProvider               // set time provider for PTS calculation
	OpClose                         // shutdown, drain and exit worker
)

func (o OpType) String() string {
	switch o {
	case OpSubscribe:
		return "Subscribe"
	case OpUnsubscribe:
		return "Unsubscribe"
	case OpFinished:
		return "Finished"
	case OpPlaying:
		return "Playing"
	case OpSetTimeProvider:
		return "SetTimeProvider"
	case OpClose:
		return "Close"
	default:
		return "Unknown"
	}
}

// Operation is a message sent to a track worker's operation channel.
type Operation struct {
	Type              OpType
	Track             *webrtc.TrackRemote
	Pub               *lksdk.RemoteTrackPublication
	RemoteParticipant *lksdk.RemoteParticipant
	Generation        uint64
	TimeProvider      gstreamer.TimeProvider
	ResultChan        chan<- subscriptionResult // for init coordination (nil after init)
}

// workerState holds the mutable state for a single track worker.
// Only accessed by the worker's goroutine, no synchronization needed.
type workerState struct {
	state      TrackState
	writer     *sdk.AppWriter
	generation uint64
}

// trackWorker manages the lifecycle of a single track.
// Each track gets its own goroutine to serialize operations and avoid cross-track blocking.
type trackWorker struct {
	trackID    string
	opChan     chan Operation // buffered channel for operations
	done       core.Fuse      // broken when worker exits
	generation atomic.Uint64  // current generation (for Playing coordination)
}

func (s *SDKSource) getOrCreateWorker(trackID string) *trackWorker {
	// Fast path - worker exists
	s.workersMu.RLock()
	w, exists := s.workers[trackID]
	s.workersMu.RUnlock()

	if exists {
		return w
	}

	// Slow path - need to create worker
	s.workersMu.Lock()
	defer s.workersMu.Unlock()

	if s.closing.Load() {
		return nil
	}

	// Double-check after acquiring write lock
	if w, exists = s.workers[trackID]; exists {
		return w
	}

	w = &trackWorker{
		trackID:    trackID,
		opChan:     make(chan Operation, 100),
		generation: atomic.Uint64{},
	}
	s.workers[trackID] = w
	go s.runWorker(w)

	return w
}

func (s *SDKSource) runWorker(w *trackWorker) {
	defer func() {
		w.done.Break()
		s.workersMu.Lock()
		delete(s.workers, w.trackID)
		s.workersMu.Unlock()
	}()
	state := &workerState{state: TrackStateIdle}

	for op := range w.opChan {
		if exit := s.processOp(w, w.trackID, state, op); exit {
			return // OpClose processed, exit immediately
		}
	}
}

func (s *SDKSource) submitOp(trackID string, op Operation) {
	if s.closing.Load() {
		return
	}

	w := s.getOrCreateWorker(trackID)
	if w == nil {
		return
	}

	logger.Debugw("submitting operation", "trackID", trackID, "op", op.Type.String())

	select {
	case w.opChan <- op:
	case <-w.done.Watch():
		// worker already exited, op dropped
	}
}

func (s *SDKSource) reportSubscribeError(isPostInit bool, resultChan chan<- subscriptionResult, trackID string, err error) {
	if isPostInit {
		s.callbacks.OnError(err)
	} else {
		s.sendInitResult(resultChan, trackID, err)
	}
}

func (s *SDKSource) validateSubscription(op Operation) error {
	// Check websocket/video incompatibility for Track requests
	if s.RequestType == types.RequestTypeTrack &&
		op.Pub.Kind() == lksdk.TrackKindVideo &&
		s.Outputs[types.EgressTypeWebsocket] != nil {
		mimeType := types.MimeType(strings.ToLower(op.Track.Codec().MimeType))
		return errors.ErrIncompatible("websocket", mimeType)
	}
	return nil
}

func (s *SDKSource) updatePreInitStateLocked(op Operation, ts *config.TrackSource) {
	// Update codec flags based on mime type
	switch ts.MimeType {
	case types.MimeTypeOpus, types.MimeTypePCMU, types.MimeTypePCMA:
		s.AudioEnabled = true
		if s.AudioOutCodec == "" {
			if ts.MimeType == types.MimeTypePCMU || ts.MimeType == types.MimeTypePCMA {
				s.AudioOutCodec = types.MimeTypeOpus
			} else {
				s.AudioOutCodec = ts.MimeType
			}
		}
		s.AudioTranscoding = true
		s.AudioTracks = append(s.AudioTracks, ts)

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
		s.VideoTrack = ts
	}

	// Set identity and filename replacements based on request type
	track := op.Track
	pub := op.Pub
	rp := op.RemoteParticipant
	switch s.RequestType {
	case types.RequestTypeTrackComposite:
		if s.Identity == "" || track.Kind() == webrtc.RTPCodecTypeVideo {
			s.Identity = rp.Identity()
			s.filenameReplacements["{publisher_identity}"] = s.Identity
		}

	case types.RequestTypeTrack:
		s.Identity = rp.Identity()
		s.TrackKind = pub.Kind().String()
		s.TrackSource = strings.ToLower(pub.Source().String())
		if o := s.GetFileConfig(); o != nil {
			o.OutputType = types.TrackOutputTypes[ts.MimeType]
		}
		s.filenameReplacements["{track_id}"] = s.TrackID
		s.filenameReplacements["{track_type}"] = s.TrackKind
		s.filenameReplacements["{track_source}"] = s.TrackSource
		s.filenameReplacements["{publisher_identity}"] = s.Identity
	}
}

func (s *SDKSource) handleSubscribe(w *trackWorker, trackID string, state *workerState, op Operation) *sdk.AppWriter {
	s.subLock.RLock()
	isPostInit := s.initialized.IsBroken()
	isPreInit := !isPostInit

	var subscribeErr error
	defer func() {
		if subscribeErr != nil {
			s.reportSubscribeError(isPostInit, op.ResultChan, trackID, subscribeErr)
		}
	}()

	// Early validation before creating writer
	if err := s.validateSubscription(op); err != nil {
		subscribeErr = err
		logger.Errorw("subscription validation failed", err, "trackID", trackID)
		s.subLock.RUnlock()
		return nil
	}

	state.generation++
	w.generation.Store(state.generation)

	writer, ts, err := s.createWriterForOp(op)
	if err != nil {
		subscribeErr = err
		logger.Errorw("failed to create writer", err, "trackID", trackID)
		s.subLock.RUnlock()
		return nil
	}

	if s.closing.Load() {
		// Release subLock before blocking drain
		s.subLock.RUnlock()
		s.handleOrphanedWriter(trackID, writer)
		return nil
	}

	s.mu.Lock()
	if isPreInit {
		s.updatePreInitStateLocked(op, ts)
	}
	s.mu.Unlock()

	// All validation passed - report success
	s.sendInitResult(op.ResultChan, trackID, nil)

	// Release subLock before transitioning to ACTIVE - we're done with pre-init work
	s.subLock.RUnlock()

	// For post-init subscriptions, notify pipeline to add track
	if isPostInit {
		<-s.callbacks.BuildReady
		s.callbacks.OnTrackAdded(ts)
	}

	writer.MarkAddedToPipeline()
	return writer
}

func (s *SDKSource) processOp(w *trackWorker, trackID string, ws *workerState, op Operation) bool {
	logger.Debugw("processing operation", "trackID", trackID, "op", op.Type.String(), "state", ws.state.String())

	switch ws.state {
	case TrackStateIdle:
		return s.processIdleOp(w, trackID, ws, op)
	case TrackStateActive:
		return s.processActiveOp(w, trackID, ws, op)
	case TrackStateCleaning:
		// Unreachable: worker blocks in startCleanup while state is CLEANING.
		// Ops queue in opChan and are processed after cleanup completes (in IDLE state).
		return false
	default:
		logger.Warnw("invalid state", nil, "trackID", trackID, "state", ws.state.String())
		return false
	}
}

func (s *SDKSource) processIdleOp(w *trackWorker, trackID string, state *workerState, op Operation) bool {
	switch op.Type {
	case OpSubscribe:
		if writer := s.handleSubscribe(w, trackID, state, op); writer != nil {
			state.state = TrackStateActive
			state.writer = writer
			s.active.Inc()
		}

	case OpPlaying:
		logger.Warnw("invalid op in IDLE", nil, "trackID", trackID, "op", op.Type.String(), "generation", op.Generation)
	case OpSetTimeProvider:
		logger.Warnw("invalid op in IDLE", nil, "trackID", trackID, "op", op.Type.String())
	case OpClose:
		logger.Warnw("invalid op in IDLE", nil, "trackID", trackID, "op", op.Type.String())
		return true
	case OpUnsubscribe, OpFinished:
		logger.Warnw("invalid op in IDLE", nil, "trackID", trackID, "op", op.Type.String())
	}
	return false
}

func (s *SDKSource) processActiveOp(_ *trackWorker, trackID string, state *workerState, op Operation) bool {
	switch op.Type {
	case OpSubscribe:
		// Not possible, double subscribe shouldn't be possible, nothing to do
		logger.Warnw("unexpected subscribe in ACTIVE state", nil, "trackID", trackID)

	case OpPlaying:
		if op.Generation == state.generation && state.writer != nil {
			state.writer.Playing()
		} else {
			logger.Warnw("playing for previous writer", nil, "trackID", trackID, "op", op.Type.String(), "generation", op.Generation)
		}

	case OpSetTimeProvider:
		if state.writer != nil {
			state.writer.SetTimeProvider(op.TimeProvider)
		}

	case OpClose:
		// Drain writer (non-blocking for shutdown)
		if state.writer != nil {
			state.writer.Drain(false)
		}
		state.writer = nil
		state.state = TrackStateIdle
		s.active.Dec()
		return true // signal worker to exit

	case OpUnsubscribe:
		state.state = TrackStateCleaning
		s.startCleanup(trackID, state)

	case OpFinished:
		// StreamStopped - immediate cleanup
		state.state = TrackStateCleaning
		s.doCleanup(trackID, state)
	}
	return false
}

// Cleanup functions

func (s *SDKSource) startCleanup(trackID string, state *workerState) {
	writer := state.writer
	writer.OnUnsubscribed()

	// Wait for writer to finish, but also handle shutdown
	select {
	case <-writer.Finished():
		// normal completion
	case <-s.closed.Watch():
		// shutdown - force drain like old CloseWriters did
		writer.Drain(false)
	}

	s.doCleanup(trackID, state)
}

func (s *SDKSource) doCleanup(trackID string, state *workerState) {
	writer := state.writer
	if writer == nil {
		// Already cleaned up (defensive guard for future code paths)
		state.state = TrackStateIdle
		return
	}
	state.writer = nil

	// Blocking cleanup - only affects this track's worker
	active := s.active.Dec()
	shouldContinue := s.RequestType == types.RequestTypeParticipant ||
		s.RequestType == types.RequestTypeRoomComposite

	if shouldContinue {
		trackKind := writer.TrackKind()
		if trackKind == webrtc.RTPCodecTypeAudio {
			writer.Drain(true)
		}
		s.sync.RemoveTrack(trackID)
		<-s.callbacks.BuildReady
		s.callbacks.OnTrackRemoved(trackID)
		if trackKind == webrtc.RTPCodecTypeVideo {
			writer.Drain(true)
		}
	} else {
		writer.Drain(true)
		if active == 0 {
			s.finished()
		}
	}

	state.state = TrackStateIdle
}

// ---------------- Helper functions ----------------

func (s *SDKSource) createWriterForOp(op Operation) (*sdk.AppWriter, *config.TrackSource, error) {
	track, pub, rp := op.Track, op.Pub, op.RemoteParticipant

	<-s.callbacks.GstReady

	src, err := gst.NewElementWithName("appsrc", fmt.Sprintf("app_%s", track.ID()))
	if err != nil {
		return nil, nil, errors.ErrGstPipelineError(err)
	}

	ts := &config.TrackSource{
		TrackID:         pub.SID(),
		TrackKind:       pub.Kind(),
		ParticipantKind: rp.Kind(),
		MimeType:        types.MimeType(strings.ToLower(track.Codec().MimeType)),
		PayloadType:     track.Codec().PayloadType,
		ClockRate:       track.Codec().ClockRate,
	}
	ts.AppSrc = app.SrcFromElement(src)

	var tc sdk.DriftHandler

	// Handle codec-specific setup (tempo controller for audio)
	switch ts.MimeType {
	case types.MimeTypeOpus, types.MimeTypePCMU, types.MimeTypePCMA:
		if s.AudioTempoController.Enabled {
			c := tempo.NewController()
			ts.TempoController = c
			tc = c
		}

	case types.MimeTypeH264, types.MimeTypeVP8, types.MimeTypeVP9:
		// Video codecs - no special setup needed here

	default:
		return nil, nil, errors.ErrNotSupported(string(ts.MimeType))
	}

	writer, err := sdk.NewAppWriter(s.PipelineConfig, track, pub, rp, ts, s.sync, tc, s.callbacks)
	if err != nil {
		return nil, nil, err
	}

	if tp := s.timeProvider.Load(); tp != nil {
		writer.SetTimeProvider(*tp)
	}

	return writer, ts, nil
}

func (s *SDKSource) handleOrphanedWriter(trackID string, writer *sdk.AppWriter) {
	writer.Drain(true)
	logger.Debugw("orphaned writer cleaned up", "trackID", trackID)
}
