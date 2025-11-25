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
	"path"
	"strings"
	"time"

	"github.com/frostbyte73/core"
	"github.com/linkdata/deadlock"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/pipeline/sink/m3u8"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

const (
	defaultLivePlaylistWindow = 5
)

type SegmentSink struct {
	*base
	*uploader.Uploader
	*config.SegmentConfig

	conf             *config.PipelineConfig
	manifestPlaylist *config.Playlist
	callbacks        *gstreamer.Callbacks

	segmentCount int
	playlist     m3u8.PlaylistWriter
	livePlaylist m3u8.PlaylistWriter

	segmentLock  deadlock.Mutex
	infoLock     deadlock.Mutex
	playlistLock deadlock.Mutex

	initialized           bool
	startTime             time.Time
	lastUpload            time.Time
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

func newSegmentSink(
	p *gstreamer.Pipeline,
	conf *config.PipelineConfig,
	o *config.SegmentConfig,
	callbacks *gstreamer.Callbacks,
	monitor *stats.HandlerMonitor,
) (*SegmentSink, error) {
	u, err := uploader.New(o.StorageConfig, conf.BackupConfig, monitor, conf.Info)
	if err != nil {
		return nil, err
	}

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

	segmentBin, err := builder.BuildSegmentBin(p, conf)
	if err != nil {
		return nil, err
	}
	if err = p.AddSinkBin(segmentBin); err != nil {
		return nil, err
	}

	maxPendingUploads := (conf.MaxUploadQueue * 60) / o.SegmentDuration
	segmentSink := &SegmentSink{
		base: &base{
			bin: segmentBin,
		},
		Uploader:              u,
		SegmentConfig:         o,
		conf:                  conf,
		callbacks:             callbacks,
		playlist:              playlist,
		livePlaylist:          livePlaylist,
		outputType:            outputType,
		openSegmentsStartTime: make(map[string]uint64),
		closedSegments:        make(chan SegmentUpdate, maxPendingUploads),
		playlistUpdates:       make(chan SegmentUpdate, maxPendingUploads),
	}

	if conf.Manifest != nil {
		segmentSink.manifestPlaylist = conf.Manifest.AddPlaylist()
	}

	// Register gauges that track the number of segments and playlist updates pending upload
	monitor.RegisterPlaylistChannelSizeGauge(segmentSink.conf.NodeID, segmentSink.conf.ClusterID, segmentSink.conf.Info.EgressId,
		func() float64 {
			return float64(len(segmentSink.playlistUpdates))
		})
	monitor.RegisterSegmentsChannelSizeGauge(segmentSink.conf.NodeID, segmentSink.conf.ClusterID, segmentSink.conf.Info.EgressId,
		func() float64 {
			return float64(len(segmentSink.closedSegments))
		})

	return segmentSink, nil
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

		location, size, err := s.Upload(segmentLocalPath, segmentStoragePath, s.outputType, true)
		if err != nil {
			s.callbacks.OnError(err)
			return
		}

		// lock segment info updates
		s.infoLock.Lock()
		s.SegmentsInfo.SegmentCount++
		s.SegmentsInfo.Size += size
		if s.manifestPlaylist != nil {
			s.manifestPlaylist.AddSegment(segmentStoragePath, location)
		}
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

	s.segmentCount++
	if s.shouldUploadPlaylist() {
		// ignore playlist upload failures until close
		_ = s.uploadPlaylist()
	}

	if s.livePlaylist != nil {
		if err := s.livePlaylist.Append(segmentStartTime, duration, update.filename); err != nil {
			return err
		}
		// ignore playlist upload failures until close
		_ = s.uploadLivePlaylist()
	}

	return nil
}

// Each segment adds about 100 bytes in the playlist, and long playlists can get very large.
// Uploads every N segments, where N is the number of hours, with a minimum frequency of once per minute
func (s *SegmentSink) shouldUploadPlaylist() bool {
	return s.lastUpload.IsZero() ||
		s.segmentCount%(int(time.Since(s.startTime)/time.Hour)+1) == 0 ||
		time.Since(s.lastUpload) > time.Minute
}

func (s *SegmentSink) uploadPlaylist() error {
	playlistLocalPath := path.Join(s.LocalDir, s.PlaylistFilename)
	playlistStoragePath := path.Join(s.StorageDir, s.PlaylistFilename)
	playlistLocation, _, err := s.Upload(playlistLocalPath, playlistStoragePath, s.OutputType, false)
	if err != nil {
		return err
	}

	s.lastUpload = time.Now()
	s.SegmentsInfo.PlaylistLocation = playlistLocation
	if s.manifestPlaylist != nil {
		s.manifestPlaylist.Location = playlistLocation
	}
	return nil
}

func (s *SegmentSink) uploadLivePlaylist() error {
	liveLocalPath := path.Join(s.LocalDir, s.LivePlaylistFilename)
	liveStoragePath := path.Join(s.StorageDir, s.LivePlaylistFilename)
	livePlaylistLocation, _, err := s.Upload(liveLocalPath, liveStoragePath, s.OutputType, false)
	if err == nil {
		s.SegmentsInfo.LivePlaylistLocation = livePlaylistLocation
	}
	return err
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

	filename := filepath[len(s.LocalDir)+1:]

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

	filename := filepath[len(s.LocalDir)+1:]

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

func (s *SegmentSink) UploadManifest(filepath string) (string, bool, error) {
	if s.DisableManifest && !s.conf.Info.BackupStorageUsed {
		return "", false, nil
	}

	storagePath := path.Join(s.StorageDir, path.Base(filepath))
	location, _, err := s.Upload(filepath, storagePath, types.OutputTypeJSON, false)
	if err != nil {
		return "", false, err
	}

	return location, true, nil
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

	return nil
}
