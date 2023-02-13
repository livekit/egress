package sink

import (
	"fmt"
	"io"
	"os"
	"path"
	"sync"
	"time"

	"github.com/grafov/m3u8"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

const maxPendingUploads = 100

type SegmentSink struct {
	uploader.Uploader

	conf *config.PipelineConfig
	*config.OutputConfig

	playlist                  *m3u8.MediaPlaylist
	currentItemStartTimestamp int64
	currentItemFilename       string
	playlistPath              string
	startDate                 time.Time
	startDateTimestamp        time.Duration

	openSegmentsStartTime map[string]int64
	openSegmentsLock      sync.Mutex

	endedSegments         chan SegmentUpdate
	segmentUploadDoneChan chan error
}

type SegmentUpdate struct {
	endTime   int64
	localPath string
}

func newSegmentSink(u uploader.Uploader, conf *config.PipelineConfig, p *config.OutputConfig) (*SegmentSink, error) {
	// "github.com/grafov/m3u8" is fairly inefficient for frequent serializations of long playlists and
	// doesn't implement recent additions to the HLS spec, but I'm not aware of anything better, short of
	// writing one.
	playlist, err := m3u8.NewMediaPlaylist(0, 15000) // 15,000 -> about 24h with 6s segments
	if err != nil {
		return nil, err
	}

	playlist.MediaType = m3u8.EVENT
	playlist.SetVersion(4) // Needed because we have float segment durations

	return &SegmentSink{
		Uploader:              u,
		OutputConfig:          p,
		conf:                  conf,
		playlist:              playlist,
		playlistPath:          p.PlaylistFilename,
		openSegmentsStartTime: make(map[string]int64),
	}, nil
}

func (s *SegmentSink) Start() error {
	s.endedSegments = make(chan SegmentUpdate, maxPendingUploads)
	s.segmentUploadDoneChan = make(chan error, 1)

	go func() {
		var err error
		defer func() {
			s.segmentUploadDoneChan <- err
		}()

		for update := range s.endedSegments {
			var size int64
			s.SegmentsInfo.SegmentCount++

			segmentStoragePath := s.GetStorageFilepath(update.localPath)
			_, size, err = s.Upload(update.localPath, segmentStoragePath, s.getSegmentOutputType())
			if err != nil {
				return
			}

			s.SegmentsInfo.Size += size

			err = s.endSegment(update.localPath, update.endTime)
			if err != nil {
				logger.Errorw("failed to end segment", err, "path", update.localPath)
				return
			}
			playlistStoragePath := s.GetStorageFilepath(s.PlaylistFilename)
			s.SegmentsInfo.PlaylistLocation, _, err = s.Upload(s.PlaylistFilename, playlistStoragePath, s.OutputType)
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
	if filepath == "" {
		return fmt.Errorf("invalid filepath")
	}

	if startTime < 0 {
		return fmt.Errorf("invalid start timestamp")
	}

	k := getFilenameFromFilePath(filepath)

	s.openSegmentsLock.Lock()
	defer s.openSegmentsLock.Unlock()

	if s.startDateTimestamp == 0 {
		s.startDateTimestamp = time.Duration(startTime)
	}

	if _, ok := s.openSegmentsStartTime[k]; ok {
		return fmt.Errorf("segment with this name already started")
	}

	s.openSegmentsStartTime[k] = startTime

	return nil
}

func (s *SegmentSink) UpdateStartDate(t time.Time) {
	s.openSegmentsLock.Lock()
	defer s.openSegmentsLock.Unlock()

	s.startDate = t
}

func (s *SegmentSink) EnqueueSegmentUpload(segmentPath string, endTime int64) error {
	select {
	case s.endedSegments <- SegmentUpdate{localPath: segmentPath, endTime: endTime}:
		return nil

	default:
		err := errors.New("segment upload job queue is full")
		logger.Infow("failed to upload segment", "error", err)
		return errors.ErrUploadFailed(segmentPath, err)
	}
}

func (s *SegmentSink) endSegment(filepath string, endTime int64) error {
	if filepath == "" {
		return fmt.Errorf("invalid filepath")
	}

	if endTime <= s.currentItemStartTimestamp {
		return fmt.Errorf("segment end time before start time")
	}

	k := getFilenameFromFilePath(filepath)

	s.openSegmentsLock.Lock()
	defer s.openSegmentsLock.Unlock()

	t, ok := s.openSegmentsStartTime[k]
	if !ok {
		return fmt.Errorf("no open segment with the name %s", k)
	}
	delete(s.openSegmentsStartTime, k)

	duration := float64(endTime-t) / float64(time.Second)

	// This assumes EndSegment will be called in the same order as StartSegment
	err := s.playlist.Append(k, duration, "")
	if err != nil {
		return err
	}

	segmentStartDate := s.startDate.Add(-s.startDateTimestamp).Add(time.Duration(t))
	err = s.playlist.SetProgramDateTime(segmentStartDate)
	if err != nil {
		return err
	}

	// Write playlist for every segment. This allows better crash recovery and to use
	// it as an Event playlist, at the cost of extra I/O
	return s.writePlaylist()
}

func getFilenameFromFilePath(filepath string) string {
	_, filename := path.Split(filepath)

	return filename
}

func (s *SegmentSink) writePlaylist() error {
	buf := s.playlist.Encode()

	file, err := os.Create(s.playlistPath)
	if err != nil {
		return nil
	}
	defer func() {
		_ = file.Close()
	}()

	_, err = io.Copy(file, buf)
	if err != nil {
		return err
	}

	return nil
}

func (s *SegmentSink) Finalize() error {
	// wait for all pending upload jobs to finish
	close(s.endedSegments)
	if s.segmentUploadDoneChan != nil {
		if err := <-s.segmentUploadDoneChan; err != nil {
			return err
		}
	}

	s.playlist.Close()
	if err := s.writePlaylist(); err != nil {
		logger.Errorw("failed to send EOS to playlist writer", err)
	}

	// upload the finalized playlist
	playlistStoragePath := s.GetStorageFilepath(s.PlaylistFilename)
	s.SegmentsInfo.PlaylistLocation, _, _ = s.Upload(s.PlaylistFilename, playlistStoragePath, s.OutputType)

	if !s.DisableManifest {
		manifestLocalPath := fmt.Sprintf("%s.json", s.PlaylistFilename)
		manifestStoragePath := fmt.Sprintf("%s.json", playlistStoragePath)
		if err := uploadManifest(s.conf, s, manifestLocalPath, manifestStoragePath); err != nil {
			return err
		}
	}

	return nil
}

func (s *SegmentSink) Cleanup() {
	if s.LocalFilepath == s.StorageFilepath {
		return
	}

	dir, _ := path.Split(s.PlaylistFilename)
	if dir != "" {
		logger.Debugw("removing temporary directory", "path", dir)
		if err := os.RemoveAll(dir); err != nil {
			logger.Errorw("could not delete temp dir", err)
		}
	}
}
