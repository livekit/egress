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
	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
)

type ImageSink struct {
	uploader.Uploader

	*config.ImageConfig

	conf      *config.PipelineConfig
	callbacks *gstreamer.Callbacks
}

func newImageSink(u uploader.Uploader, p *config.PipelineConfig, o *config.ImageConfig, callbacks *gstreamer.Callbacks) (*ImageSink, error) {
	return &ImageSink{
		Uploader:    u,
		ImageConfig: o,
		conf:        p,
		callbacks:   callbacks,
	}, nil
}

func (s *ImageSink) Start() error {
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
