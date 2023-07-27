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
	"github.com/livekit/egress/pkg/pipeline/sink/m3u8"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

const maxPendingUploads = 100

type SegmentSink struct {
	uploader.Uploader

	conf *config.PipelineConfig
	*config.SegmentConfig

	playlist                  *m3u8.PlaylistWriter
	currentItemStartTimestamp int64
	currentItemFilename       string
	startDate                 time.Time
	startDateTimestamp        time.Duration

	openSegmentsStartTime map[string]int64
	openSegmentsLock      sync.Mutex

	endedSegments chan SegmentUpdate
	done          core.Fuse
}

type SegmentUpdate struct {
	endTime  int64
	filename string
}

func newSegmentSink(u uploader.Uploader, p *config.PipelineConfig, o *config.SegmentConfig) (*SegmentSink, error) {
	playlistName := path.Join(o.LocalDir, o.PlaylistFilename)
	playlist, err := m3u8.NewPlaylistWriter(playlistName, o.SegmentDuration)
	if err != nil {
		return nil, err
	}

	return &SegmentSink{
		Uploader:              u,
		SegmentConfig:         o,
		conf:                  p,
		playlist:              playlist,
		openSegmentsStartTime: make(map[string]int64),
		endedSegments:         make(chan SegmentUpdate, maxPendingUploads),
		done:                  core.NewFuse(),
		startDateTimestamp:    -1,
	}, nil
}

func (s *SegmentSink) Start() error {
	go func() {
		var err error
		defer func() {
			if err != nil {
				s.conf.OnFailure(err)
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

func (s *SegmentSink) StartSegment(filepath string, startTime int64) error {
	if !strings.HasPrefix(filepath, s.LocalDir) {
		return fmt.Errorf("invalid filepath")
	}

	filename := filepath[len(s.LocalDir):]

	if startTime < 0 {
		return fmt.Errorf("invalid start timestamp")
	}

	s.openSegmentsLock.Lock()
	defer s.openSegmentsLock.Unlock()

	if s.startDateTimestamp < 0 {
		s.startDateTimestamp = time.Duration(startTime)
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

	s.startDate = t
}

func (s *SegmentSink) EnqueueSegmentUpload(filepath string, endTime int64) error {
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

func (s *SegmentSink) endSegment(filename string, endTime int64) error {
	if endTime <= s.currentItemStartTimestamp {
		return fmt.Errorf("segment end time before start time")
	}

	s.openSegmentsLock.Lock()
	defer s.openSegmentsLock.Unlock()

	t, ok := s.openSegmentsStartTime[filename]
	if !ok {
		return fmt.Errorf("no open segment with the name %s", filename)
	}
	delete(s.openSegmentsStartTime, filename)

	duration := float64(endTime-t) / float64(time.Second)

	segmentStartDate := s.startDate.Add(-s.startDateTimestamp).Add(time.Duration(t))
	if err := s.playlist.Append(segmentStartDate, duration, filename); err != nil {
		return err
	}

	return nil
}

func (s *SegmentSink) Finalize() error {
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
