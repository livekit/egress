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
	"context"
	"time"

	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/server-sdk-go/v2/datatrack"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
)

func (s *SDKSource) initDataTracks() {
	s.publishedDataTracks = make(map[string]error)
	s.dataCtx, s.dataCancel = context.WithCancel(context.Background())
}

// onDataTrackPublished runs on the signal goroutine, where Subscribe would block on its own response
func (s *SDKSource) onDataTrackPublished(track *datatrack.RemoteTrack, _ *lksdk.RemoteParticipant) {
	info := track.Info()
	identity := track.PublisherIdentity()

	s.dataMu.Lock()
	defer s.dataMu.Unlock()

	trackID := string(info.SID)
	s.publishedDataTracks[trackID] = nil
	if !s.MatchesMediaData(trackID, info.Name, identity) {
		return
	}
	if info.UsesE2EE {
		logger.Warnw("skipping e2ee data track", nil, "trackID", trackID)
		s.publishedDataTracks[trackID] = errors.ErrNotSupported("e2ee data track " + trackID)
		return
	}
	// checked under dataMu so no reader starts after closeDataTracks begins waiting
	if s.dataCtx.Err() != nil {
		return
	}
	s.dataWG.Add(1)
	go s.recordDataTrack(track, identity, info)
}

func (s *SDKSource) recordDataTrack(track *datatrack.RemoteTrack, identity string, info datatrack.Info) {
	defer s.dataWG.Done()
	trackID := string(info.SID)

	stream, err := track.Subscribe(s.dataCtx)
	if err != nil {
		logger.Warnw("failed to subscribe to data track", err, "trackID", trackID)
		return
	}
	defer stream.Close()
	logger.Infow("subscribed to data track", "trackID", trackID, "name", info.Name)
	s.startDataRecording()

	for {
		frame, err := stream.Next(s.dataCtx)
		if err != nil {
			break
		}
		if err = s.callbacks.OnDataFrame(types.DataFrame{
			ParticipantIdentity: identity,
			Info:                info,
			Time:                dataFrameTime(frame, time.Now()),
			Frame:               frame,
		}); err != nil {
			s.callbacks.OnError(err)
			break
		}
	}
	s.callbacks.OnDataTrackEnded(trackID)
}

// data tracks carry no sender reports yet, so frames are timed on receipt
func dataFrameTime(_ datatrack.Frame, receivedAt time.Time) time.Time {
	return receivedAt
}

// startDataRecording starts a data-only request, whose synchronizer never starts without RTP
func (s *SDKSource) startDataRecording() {
	if s.AudioEnabled || s.VideoEnabled {
		return
	}
	s.dataStartedAt.CompareAndSwap(0, time.Now().UnixNano())
	s.startRecording.Break()
}

func (s *SDKSource) awaitDataTrackPublication(trackID string, deadline time.Time) error {
	for time.Now().Before(deadline) {
		s.dataMu.Lock()
		err, ok := s.publishedDataTracks[trackID]
		s.dataMu.Unlock()
		if ok {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.ErrTrackNotFound(trackID)
}

// buffered frames are still written, since Stream.Next drains them before reporting ctx.Err
func (s *SDKSource) closeDataTracks() {
	s.dataMu.Lock()
	s.dataCancel()
	s.dataMu.Unlock()
	s.dataWG.Wait()
}
