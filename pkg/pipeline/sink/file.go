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
	conf *config.PipelineConfig
	out  *config.OutputConfig
}

func newFileSink(u uploader.Uploader, conf *config.PipelineConfig, out *config.OutputConfig) *FileSink {
	return &FileSink{
		Uploader: u,
		conf:     conf,
		out:      out,
	}
}

func (s *FileSink) Start() error {
	return nil
}

func (s *FileSink) Close() error {
	if s.Uploader == nil {
		return nil
	}

	location, size, err := s.Upload(s.out.LocalFilepath, s.out.StorageFilepath, s.out.OutputType)
	if err != nil {
		return err
	}
	s.out.FileInfo.Location = location
	s.out.FileInfo.Size = size

	if !s.out.DisableManifest {
		manifestLocalPath := fmt.Sprintf("%s.json", s.out.LocalFilepath)
		manifestStoragePath := fmt.Sprintf("%s.json", s.out.StorageFilepath)
		if err = uploadManifest(s.conf, s, manifestLocalPath, manifestStoragePath); err != nil {
			return err
		}
	}

	return nil
}

func (s *FileSink) Cleanup() {
	if s.Uploader == nil {
		return
	}

	dir, _ := path.Split(s.out.LocalFilepath)
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
