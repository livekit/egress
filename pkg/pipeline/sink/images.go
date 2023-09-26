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

	"github.com/frostbyte73/core"
	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type ImageSink struct {
	uploader.Uploader

	*config.ImageConfig

	conf      *config.PipelineConfig
	callbacks *gstreamer.Callbacks

	createdImages chan ImageUpdate
	done          core.Fuse
}

type ImageUpdate struct {
	timestamp int64
	filename  string
}

func newImageSink(u uploader.Uploader, p *config.PipelineConfig, o *config.ImageConfig, callbacks *gstreamer.Callbacks) (*ImageSink, error) {
	return &ImageSink{
		Uploader:    u,
		ImageConfig: o,
		conf:        p,
		callbacks:   callbacks,

		createdImages: make(chan ImageUpdate, maxPendingUploads),
		done:          core.NewFuse(),
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
			s.ImagesInfo.ImagesCount++

			filename := update.filename
			imageLocalPath := path.Join(s.LocalDir, filename)
			if s.ImageSuffix == livekit.ImageFileSuffix_IMAGE_SUFFIX_TIMESTAMP {
				location := fmt.Sprintf("%s_%%05d%s", c.ImagePrefix, types.FileExtensionForOutputType[c.OutputType])
				newFilemame := fmt.Sprintf("%s_%s%03d.ts", s.ImagePrefix, ts.Format("20060102150405"), ts.UnixMilli()%1000, types.FileExtensionForOutputType[s.OutputType])
			}

			imageStoragePath := path.Join(s.StorageDir, filename)

			_, size, err = s.Upload(imageLocalPath, imageStoragePath, s.getImageOutputType(), true)
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

	// TODO setup gst pipeline
	// TODO filename

	return nil
}

func (s *ImageSink) NewImage(filepath string, ts uint64) error {
	// TODO rename file, upload

	return nil
}

func (s *ImageSink) Close() error {

	return nil
}

func (*ImageSink) Cleanup() {
}
