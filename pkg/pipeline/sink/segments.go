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

package sink

import (
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/frostbyte73/core"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/sink/m3u8"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

const (
	maxPendingUploads         = 100
	defaultLivePlaylistWindow = 5
)

type SegmentSink struct {
	uploader.Uploader

	*config.SegmentConfig
	conf      *config.PipelineConfig
	callbacks *gstreamer.Callbacks

	playlist     m3u8.PlaylistWriter
	livePlaylist m3u8.PlaylistWriter

	segmentLock  sync.Mutex
	infoLock     sync.Mutex
	playlistLock sync.Mutex

	initialized           bool
	startTime             time.Time
	outputType            types.OutputType
	startRunningTime      uint64
	openSegmentsStartTime map[string]uint64

	closedSegments  chan SegmentUpdate
	playlistUpdates chan SegmentUpdate
	done            core.Fuse
}

type SegmentUpdate struct {
	endTime        uint64
	filename       string
	uploadComplete chan struct{}
}

func newSegmentSink(u uploader.Uploader, p *config.PipelineConfig, o *config.SegmentConfig, callbacks *gstreamer.Callbacks, monitor *stats.HandlerMonitor) (*SegmentSink, error) {
	playlistName := path.Join(o.LocalDir, o.PlaylistFilename)
	playlist, err := m3u8.NewEventPlaylistWriter(playlistName, o.SegmentDuration)
	if err != nil {
		return nil, err
	}

	var livePlaylist m3u8.PlaylistWriter
	if o.LivePlaylistFilename != "" {
		playlistName = path.Join(o.LocalDir, o.LivePlaylistFilename)
		livePlaylist, err = m3u8.NewLivePlaylistWriter(playlistName, o.SegmentDuration, defaultLivePlaylistWindow)
		if err != nil {
			return nil, err
		}
	}

	outputType := o.OutputType
	if outputType == types.OutputTypeHLS {
		outputType = types.OutputTypeTS
	}

	s := &SegmentSink{
		Uploader:              u,
		SegmentConfig:         o,
		conf:                  p,
		callbacks:             callbacks,
		playlist:              playlist,
		livePlaylist:          livePlaylist,
		outputType:            outputType,
		openSegmentsStartTime: make(map[string]uint64),
		closedSegments:        make(chan SegmentUpdate, maxPendingUploads),
		playlistUpdates:       make(chan SegmentUpdate, maxPendingUploads),
	}

	// Register gauges that track the number of segments and playlist updates pending upload
	monitor.RegisterPlaylistChannelSizeGauge(s.conf.NodeID, s.conf.ClusterID, s.conf.Info.EgressId,
		func() float64 {
			return float64(len(s.playlistUpdates))
		})
	monitor.RegisterSegmentsChannelSizeGauge(s.conf.NodeID, s.conf.ClusterID, s.conf.Info.EgressId,
		func() float64 {
			return float64(len(s.closedSegments))
		})

	return s, nil
}

func (s *SegmentSink) Start() error {
	go func() {
		defer close(s.playlistUpdates)
		for update := range s.closedSegments {
			s.handleClosedSegment(update)
		}
	}()

	go func() {
		defer s.done.Break()
		for update := range s.playlistUpdates {
			if err := s.handlePlaylistUpdates(update); err != nil {
				s.callbacks.OnError(err)
				return
			}
		}
	}()

	return nil
}

func (s *SegmentSink) handleClosedSegment(update SegmentUpdate) {
	// keep playlist updates in order
	s.playlistUpdates <- update

	segmentLocalPath := path.Join(s.LocalDir, update.filename)
	segmentStoragePath := path.Join(s.StorageDir, update.filename)

	// upload in parallel
	go func() {
		defer close(update.uploadComplete)

		_, size, err := s.Upload(segmentLocalPath, segmentStoragePath, s.outputType, true, "segment")
		if err != nil {
			s.callbacks.OnError(err)
			return
		}

		// lock segment info updates
		s.infoLock.Lock()
		s.SegmentsInfo.SegmentCount++
		s.SegmentsInfo.Size += size
		s.infoLock.Unlock()
	}()
}

func (s *SegmentSink) handlePlaylistUpdates(update SegmentUpdate) error {
	s.segmentLock.Lock()
	t, ok := s.openSegmentsStartTime[update.filename]
	if !ok {
		s.segmentLock.Unlock()
		return fmt.Errorf("no open segment with the name %s", update.filename)
	}
	delete(s.openSegmentsStartTime, update.filename)
	s.segmentLock.Unlock()

	duration := float64(time.Duration(update.endTime-t)) / float64(time.Second)
	segmentStartTime := s.startTime.Add(time.Duration(t - s.startRunningTime))

	// do not update playlist until upload is complete
	<-update.uploadComplete

	s.playlistLock.Lock()
	defer s.playlistLock.Unlock()

	if err := s.playlist.Append(segmentStartTime, duration, update.filename); err != nil {
		return err
	}
	if err := s.uploadPlaylist(); err != nil {
		s.callbacks.OnError(err)
	}
	if s.livePlaylist != nil {
		if err := s.livePlaylist.Append(segmentStartTime, duration, update.filename); err != nil {
			return err
		}
		if err := s.uploadLivePlaylist(); err != nil {
			s.callbacks.OnError(err)
		}
	}

	return nil
}

func (s *SegmentSink) UpdateStartDate(t time.Time) {
	s.segmentLock.Lock()
	defer s.segmentLock.Unlock()

	s.startTime = t
}

func (s *SegmentSink) FragmentOpened(filepath string, startTime uint64) error {
	if !strings.HasPrefix(filepath, s.LocalDir) {
		return fmt.Errorf("invalid filepath")
	}

	filename := filepath[len(s.LocalDir):]

	s.segmentLock.Lock()
	defer s.segmentLock.Unlock()

	if !s.initialized {
		s.initialized = true
		s.startRunningTime = startTime
	}

	if _, ok := s.openSegmentsStartTime[filename]; ok {
		return fmt.Errorf("segment with this name already started")
	}

	s.openSegmentsStartTime[filename] = startTime
	return nil
}

func (s *SegmentSink) FragmentClosed(filepath string, endTime uint64) error {
	if !strings.HasPrefix(filepath, s.LocalDir) {
		return fmt.Errorf("invalid filepath")
	}

	filename := filepath[len(s.LocalDir):]

	select {
	case s.closedSegments <- SegmentUpdate{
		filename:       filename,
		endTime:        endTime,
		uploadComplete: make(chan struct{}),
	}:
		return nil

	default:
		err := errors.New("segment upload job queue is full")
		logger.Infow("failed to upload segment", "error", err)
		return errors.ErrUploadFailed(filename, err)
	}
}

func (s *SegmentSink) Close() error {
	// wait for pending jobs to finish
	close(s.closedSegments)
	<-s.done.Watch()

	s.playlistLock.Lock()
	defer s.playlistLock.Unlock()

	if err := s.playlist.Close(); err != nil {
		return err
	}
	if err := s.uploadPlaylist(); err != nil {
		return err
	}

	if s.livePlaylist != nil {
		if err := s.livePlaylist.Close(); err != nil {
			return err
		}
		if err := s.uploadLivePlaylist(); err != nil {
			return err
		}
	}

	if !s.DisableManifest {
		playlistLocalPath := path.Join(s.LocalDir, s.PlaylistFilename)
		playlistStoragePath := path.Join(s.StorageDir, s.PlaylistFilename)
		manifestLocalPath := fmt.Sprintf("%s.json", playlistLocalPath)
		manifestStoragePath := fmt.Sprintf("%s.json", playlistStoragePath)
		if err := uploadManifest(s.conf, s.Uploader, manifestLocalPath, manifestStoragePath); err != nil {
			return err
		}
	}

	return nil
}

func (s *SegmentSink) Cleanup() {
	if s.LocalDir == s.StorageDir {
		return
	}

	if s.LocalDir != "" {
		logger.Debugw("removing temporary directory", "path", s.LocalDir)
		if err := os.RemoveAll(s.LocalDir); err != nil {
			logger.Errorw("could not delete temp dir", err)
		}
	}
}

func (s *SegmentSink) uploadPlaylist() error {
	var err error
	playlistLocalPath := path.Join(s.LocalDir, s.PlaylistFilename)
	playlistStoragePath := path.Join(s.StorageDir, s.PlaylistFilename)
	s.SegmentsInfo.PlaylistLocation, _, err = s.Upload(playlistLocalPath, playlistStoragePath, s.OutputType, false, "playlist")
	return err
}

func (s *SegmentSink) uploadLivePlaylist() error {
	var err error
	liveLocalPath := path.Join(s.LocalDir, s.LivePlaylistFilename)
	liveStoragePath := path.Join(s.StorageDir, s.LivePlaylistFilename)
	s.SegmentsInfo.LivePlaylistLocation, _, err = s.Upload(liveLocalPath, liveStoragePath, s.OutputType, false, "live_playlist")
	return err
}
