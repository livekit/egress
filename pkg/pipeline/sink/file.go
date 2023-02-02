package sink

import (
	"fmt"
	"os"
	"path"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

type FileSink struct {
	uploader.Uploader
	*config.OutputConfig
	conf *config.PipelineConfig
}

func newFileSink(u uploader.Uploader, conf *config.PipelineConfig, out *config.OutputConfig) *FileSink {
	return &FileSink{
		Uploader:     u,
		OutputConfig: out,
		conf:         conf,
	}
}

func (s *FileSink) Start() error {
	return nil
}

func (s *FileSink) Close() error {
	location, size, err := s.Upload(s.LocalFilepath, s.StorageFilepath, s.OutputType)
	if err != nil {
		return err
	}
	s.FileInfo.Location = location
	s.FileInfo.Size = size

	if !s.DisableManifest {
		manifestLocalPath := fmt.Sprintf("%s.json", s.LocalFilepath)
		manifestStoragePath := fmt.Sprintf("%s.json", s.StorageFilepath)
		if err = uploadManifest(s.conf, s, manifestLocalPath, manifestStoragePath); err != nil {
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

func uploadManifest(conf *config.PipelineConfig, u uploader.Uploader, localFilepath, storageFilepath string) error {
	manifest, err := os.Create(localFilepath)
	if err != nil {
		return err
	}

	b, err := conf.GetManifest(types.EgressTypeFile)
	if err != nil {
		return err
	}

	_, err = manifest.Write(b)
	if err != nil {
		return err
	}

	_, _, err = u.Upload(localFilepath, storageFilepath, types.OutputTypeJSON)
	return err
}
