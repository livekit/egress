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
	"path"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/types"
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

func (s *FileSink) Close() error {
	location, size, err := s.Upload(s.LocalFilepath, s.StorageFilepath, s.OutputType, false)
	if err != nil {
		return err
	}

	s.FileInfo.Location = location
	s.FileInfo.Size = size

	if s.conf.Manifest != nil {
		s.conf.Manifest.AddFile(s.StorageFilepath, location)
	}

	return nil
}

func (s *FileSink) UploadManifest(filepath string) (string, bool, error) {
	if s.DisableManifest && !s.conf.Info.BackupStorageUsed {
		return "", false, nil
	}

	storagePath := path.Join(path.Dir(s.StorageFilepath), path.Base(filepath))
	location, _, err := s.Upload(filepath, storagePath, types.OutputTypeJSON, false)
	if err != nil {
		return "", false, err
	}

	return location, true, nil
}
