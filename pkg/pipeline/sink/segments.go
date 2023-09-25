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

	playlist         m3u8.PlaylistWriter
	livePlaylist     m3u8.PlaylistWriter
	initialized      bool
	startTime        time.Time
	startRunningTime uint64

	openSegmentsStartTime map[string]uint64
	openSegmentsLock      sync.Mutex

	endedSegments chan SegmentUpdate
	done          core.Fuse
}

type SegmentUpdate struct {
	endTime  uint64
	filename string
}

func newSegmentSink(u uploader.Uploader, p *config.PipelineConfig, o *config.SegmentConfig, callbacks *gstreamer.Callbacks) (*SegmentSink, error) {
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

	return &SegmentSink{
		Uploader:              u,
		SegmentConfig:         o,
		conf:                  p,
		callbacks:             callbacks,
		playlist:              playlist,
		livePlaylist:          livePlaylist,
		openSegmentsStartTime: make(map[string]uint64),
		endedSegments:         make(chan SegmentUpdate, maxPendingUploads),
		done:                  core.NewFuse(),
	}, nil
}

func (s *SegmentSink) Start() error {
	go func() {
		var err error
		defer func() {
			if err != nil {
				s.callbacks.OnError(err)
			}
			s.done.Break()
		}()

		for update := range s.endedSegments {
			var size int64
			s.SegmentsInfo.SegmentCount++

			segmentLocalPath := path.Join(s.LocalDir, update.filename)
			segmentStoragePath := path.Join(s.StorageDir, update.filename)
			_, size, err = s.Upload(segmentLocalPath, segmentStoragePath, s.getSegmentOutputType(), true)
			if err != nil {
				return
			}

			s.SegmentsInfo.Size += size

			err = s.endSegment(update.filename, update.endTime)
			if err != nil {
				logger.Errorw("failed to end segment", err, "path", segmentLocalPath)
				return
			}

			playlistLocalPath := path.Join(s.LocalDir, s.PlaylistFilename)
			playlistStoragePath := path.Join(s.StorageDir, s.PlaylistFilename)
			s.SegmentsInfo.PlaylistLocation, _, err = s.Upload(playlistLocalPath, playlistStoragePath, s.OutputType, false)
			if err != nil {
				return
			}

			if s.LivePlaylistFilename != "" {
				playlistLocalPath = path.Join(s.LocalDir, s.LivePlaylistFilename)
				playlistStoragePath = path.Join(s.StorageDir, s.LivePlaylistFilename)
				s.SegmentsInfo.LivePlaylistLocation, _, err = s.Upload(playlistLocalPath, playlistStoragePath, s.OutputType, false)
				if err != nil {
					return
				}
			}
		}
	}()

	return nil
}

func (s *SegmentSink) getSegmentOutputType() types.OutputType {
	switch s.OutputType {
	case types.OutputTypeHLS:
		// HLS is always mpeg ts for now. We may implement fmp4 in the future
		return types.OutputTypeTS
	default:
		return s.OutputType
	}
}

func (s *SegmentSink) StartSegment(filepath string, startTime uint64) error {
	if !strings.HasPrefix(filepath, s.LocalDir) {
		return fmt.Errorf("invalid filepath")
	}

	filename := filepath[len(s.LocalDir):]

	s.openSegmentsLock.Lock()
	defer s.openSegmentsLock.Unlock()

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

func (s *SegmentSink) UpdateStartDate(t time.Time) {
	s.openSegmentsLock.Lock()
	defer s.openSegmentsLock.Unlock()

	s.startTime = t
}

func (s *SegmentSink) EnqueueSegmentUpload(filepath string, endTime uint64) error {
	if !strings.HasPrefix(filepath, s.LocalDir) {
		return fmt.Errorf("invalid filepath")
	}

	filename := filepath[len(s.LocalDir):]

	select {
	case s.endedSegments <- SegmentUpdate{filename: filename, endTime: endTime}:
		return nil

	default:
		err := errors.New("segment upload job queue is full")
		logger.Infow("failed to upload segment", "error", err)
		return errors.ErrUploadFailed(filename, err)
	}
}

func (s *SegmentSink) endSegment(filename string, endTime uint64) error {
	s.openSegmentsLock.Lock()
	defer s.openSegmentsLock.Unlock()

	t, ok := s.openSegmentsStartTime[filename]
	if !ok {
		return fmt.Errorf("no open segment with the name %s", filename)
	}
	delete(s.openSegmentsStartTime, filename)

	duration := float64(time.Duration(endTime-t) / time.Second)
	segmentStartTime := s.startTime.Add(time.Duration(t - s.startRunningTime))
	if err := s.playlist.Append(segmentStartTime, duration, filename); err != nil {
		return err
	}
	if s.livePlaylist != nil {
		if err := s.livePlaylist.Append(segmentStartTime, duration, filename); err != nil {
			return err
		}
	}

	return nil
}

func (s *SegmentSink) Close() error {
	// wait for all pending upload jobs to finish
	close(s.endedSegments)
	<-s.done.Watch()

	if err := s.playlist.Close(); err != nil {
		logger.Errorw("failed to send EOS to playlist writer", err)
	}

	// upload the finalized playlist
	playlistLocalPath := path.Join(s.LocalDir, s.PlaylistFilename)
	playlistStoragePath := path.Join(s.StorageDir, s.PlaylistFilename)
	s.SegmentsInfo.PlaylistLocation, _, _ = s.Upload(playlistLocalPath, playlistStoragePath, s.OutputType, false)

	if s.livePlaylist != nil {
		if err := s.livePlaylist.Close(); err != nil {
			logger.Errorw("failed to send EOS to live playlist writer", err)
		}

		// upload the finalized live playlist
		playlistLocalPath = path.Join(s.LocalDir, s.LivePlaylistFilename)
		playlistStoragePath = path.Join(s.StorageDir, s.LivePlaylistFilename)
		s.SegmentsInfo.LivePlaylistLocation, _, _ = s.Upload(playlistLocalPath, playlistStoragePath, s.OutputType, false)
	}

	if !s.DisableManifest {
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
