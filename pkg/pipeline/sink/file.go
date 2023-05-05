package sink

import (
	"fmt"
	"os"
	"path"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/protocol/logger"
)

type FileSink struct {
	*uploader.Uploader

	conf *config.PipelineConfig
	*config.FileConfig
}

func newFileSink(u *uploader.Uploader, conf *config.PipelineConfig, o *config.FileConfig) *FileSink {
	return &FileSink{
		Uploader:   u,
		conf:       conf,
		FileConfig: o,
	}
}

func (s *FileSink) Start() error {
	return nil
}

func (s *FileSink) Finalize() error {
	location, size, err := s.Upload(s.LocalFilepath, s.StorageFilepath, s.OutputType)
	if err != nil {
		return err
	}
	s.FileInfo.Location = location
	s.FileInfo.Size = size

	if !s.DisableManifest {
		manifestLocalPath := fmt.Sprintf("%s.json", s.LocalFilepath)
		manifestStoragePath := fmt.Sprintf("%s.json", s.StorageFilepath)
		if err = uploadManifest(s.conf, s.Uploader, manifestLocalPath, manifestStoragePath); err != nil {
			return err
		}
	}

	return nil
}

func (s *FileSink) Cleanup() {
	if s.LocalFilepath == s.StorageFilepath {
		return
	}

	dir, _ := path.Split(s.LocalFilepath)
	if dir != "" {
		logger.Debugw("removing temporary directory", "path", dir)
		if err := os.RemoveAll(dir); err != nil {
			logger.Errorw("could not delete temp dir", err)
		}
	}
}
