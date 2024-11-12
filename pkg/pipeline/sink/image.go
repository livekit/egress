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
	"time"

	"github.com/frostbyte73/core"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type ImageSink struct {
	*uploader.Uploader

	*config.ImageConfig

	conf      *config.PipelineConfig
	callbacks *gstreamer.Callbacks

	initialized      bool
	startTime        time.Time
	startRunningTime uint64

	createdImages chan *imageUpdate
	done          core.Fuse
}

type imageUpdate struct {
	timestamp uint64
	filename  string
}

func newImageSink(u *uploader.Uploader, p *config.PipelineConfig, o *config.ImageConfig, callbacks *gstreamer.Callbacks) (*ImageSink, error) {
	maxPendingUploads := (p.MaxUploadQueue * 60) / int(o.CaptureInterval)
	return &ImageSink{
		Uploader:    u,
		ImageConfig: o,
		conf:        p,
		callbacks:   callbacks,

		createdImages: make(chan *imageUpdate, maxPendingUploads),
	}, nil
}

func (s *ImageSink) Start() error {
	go func() {
		var err error
		defer func() {
			if err != nil {
				s.callbacks.OnError(err)
			}
			s.done.Break()
		}()

		for update := range s.createdImages {
			err = s.handleNewImage(update)
			if err != nil {
				logger.Errorw("new image handling failed", err)
				return
			}
		}
	}()

	return nil
}

func (s *ImageSink) handleNewImage(update *imageUpdate) error {
	s.ImagesInfo.ImageCount++

	filename := update.filename
	ts := s.getImageTime(update.timestamp)
	imageLocalPath := path.Join(s.LocalDir, filename)
	if s.ImageSuffix == livekit.ImageFileSuffix_IMAGE_SUFFIX_TIMESTAMP {
		newFilename := fmt.Sprintf("%s_%s%03d%s", s.ImagePrefix, ts.Format("20060102150405"), ts.UnixMilli()%1000, types.FileExtensionForOutputType[s.OutputType])
		newImageLocalPath := path.Join(s.LocalDir, newFilename)

		err := os.Rename(imageLocalPath, newImageLocalPath)
		if err != nil {
			return err
		}
		filename = newFilename
		imageLocalPath = newImageLocalPath
	}

	imageStoragePath := path.Join(s.StorageDir, filename)

	location, _, err := s.Upload(imageLocalPath, imageStoragePath, s.OutputType, true)
	if err != nil {
		return err
	}

	if s.conf.Manifest != nil {
		s.conf.Manifest.AddImage(imageStoragePath, ts, location)
	}

	return nil
}

func (s *ImageSink) getImageTime(pts uint64) time.Time {
	if !s.initialized {
		s.startTime = time.Now()
		s.startRunningTime = pts
		s.initialized = true
	}

	return s.startTime.Add(time.Duration(pts - s.startRunningTime))
}

func (s *ImageSink) NewImage(filepath string, ts uint64) error {
	if !strings.HasPrefix(filepath, s.LocalDir) {
		return fmt.Errorf("invalid filepath")
	}

	filename := filepath[len(s.LocalDir)+1:]

	s.createdImages <- &imageUpdate{
		filename:  filename,
		timestamp: ts,
	}

	return nil
}

func (s *ImageSink) Close() error {
	close(s.createdImages)
	<-s.done.Watch()

	return nil
}

func (s *ImageSink) UploadManifest(filepath string) (string, bool, error) {
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
