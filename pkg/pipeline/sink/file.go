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
	"context"
	"os"
	"path"
	"time"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

type FileSink struct {
	*base
	*config.FileConfig
	*uploader.Uploader

	conf *config.PipelineConfig

	cancelSizeLogger context.CancelFunc
	sizeLoggerCtx    context.Context
}

func newFileSink(
	p *gstreamer.Pipeline,
	conf *config.PipelineConfig,
	o *config.FileConfig,
	monitor *stats.HandlerMonitor,
) (*FileSink, error) {
	u, err := uploader.New(o.StorageConfig, conf.BackupConfig, monitor, conf.Info)
	if err != nil {
		return nil, err
	}

	fileBin, err := builder.BuildFileBin(p, conf)
	if err != nil {
		return nil, err
	}
	if err = p.AddSinkBin(fileBin); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.AddOnStop(func() error {
		cancel()
		return nil
	})

	return &FileSink{
		base: &base{
			bin: fileBin,
		},
		FileConfig:       o,
		Uploader:         u,
		conf:             conf,
		cancelSizeLogger: cancel,
		sizeLoggerCtx:    ctx,
	}, nil
}

func (s *FileSink) Start() error {
	go s.monitorFileSize(s.sizeLoggerCtx)
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

func (s *FileSink) Close() error {
	if s.cancelSizeLogger != nil {
		s.cancelSizeLogger()
	}

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

func (s *FileSink) monitorFileSize(ctx context.Context) {
	thresholds := []int64{
		1 << 30,  // 1GB
		3 << 30,  // 3GB
		5 << 30,  // 5GB
		10 << 30, // 10GB
		20 << 30, // 20GB
		50 << 30, // 50GB
	}

	ticker := time.NewTicker(time.Second * 30)
	defer ticker.Stop()

	nextThreshold := 0
	statErrorLogged := false

	for nextThreshold < len(thresholds) {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(s.LocalFilepath)
			if err != nil {
				if !statErrorLogged && !errors.Is(err, os.ErrNotExist) {
					logger.Debugw("failed to stat filesink output", err, "filepath", s.LocalFilepath)
					statErrorLogged = true
				}
				continue
			}
			statErrorLogged = false

			pos := info.Size()
			for nextThreshold < len(thresholds) && pos >= thresholds[nextThreshold] {
				logger.Debugw(
					"filesink size threshold exceeded",
					"filepath", s.LocalFilepath,
					"bytesWritten", pos,
					"thresholdBytes", thresholds[nextThreshold],
				)
				nextThreshold++
			}
		}
	}
}
