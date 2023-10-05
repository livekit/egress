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

package config

import (
	"fmt"
	"os"
	"path"
	"time"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

type ImageConfig struct {
	outputConfig

	Id string // Used internally to map a gst Bin/element back to a sink and as part of the path

	ImagesInfo     *livekit.ImagesInfo
	LocalDir       string
	StorageDir     string
	ImagePrefix    string
	ImageSuffix    livekit.ImageFileSuffix
	ImageExtension types.FileExtension

	DisableManifest bool
	UploadConfig    UploadConfig

	CaptureInterval uint32
	Width           int32
	Height          int32
	ImageOutCodec   types.MimeType
}

func (p *PipelineConfig) GetImageConfigs() []*ImageConfig {
	o, _ := p.Outputs[types.EgressTypeImages]

	var configs []*ImageConfig
	for _, c := range o {
		configs = append(configs, c.(*ImageConfig))
	}

	return configs
}

func (p *PipelineConfig) getImageConfig(images *livekit.ImageOutput) (*ImageConfig, error) {
	outCodec, outputType, err := getMimeTypes(images.ImageCodec)
	if err != nil {
		return nil, err
	}

	conf := &ImageConfig{
		outputConfig: outputConfig{
			OutputType: outputType,
		},

		Id:              utils.NewGuid(""),
		ImagesInfo:      &livekit.ImagesInfo{},
		ImagePrefix:     clean(images.FilenamePrefix),
		ImageSuffix:     images.FilenameSuffix,
		DisableManifest: images.DisableManifest,
		UploadConfig:    p.getUploadConfig(images),
		CaptureInterval: images.CaptureInterval,
		Width:           images.Width,
		Height:          images.Height,
		ImageOutCodec:   outCodec,
	}

	if conf.CaptureInterval == 0 {
		// 10s by default
		conf.CaptureInterval = 10
	}

	// Set default dimensions for RoomComposite and Web. For all SDKs input, default will be
	// set from the track dimensions
	switch p.Info.Request.(type) {
	case *livekit.EgressInfo_RoomComposite, *livekit.EgressInfo_Web:
		if conf.Width == 0 {
			conf.Width = p.Width
		}
		if conf.Height == 0 {
			conf.Height = p.Height
		}
	}

	// filename
	err = conf.updatePrefix(p)
	if err != nil {
		return nil, err
	}

	return conf, nil
}

func (o *ImageConfig) updatePrefix(p *PipelineConfig) error {
	identifier, replacements := p.getFilenameInfo()

	o.ImagePrefix = stringReplace(o.ImagePrefix, replacements)

	o.ImageExtension = types.FileExtensionForOutputType[o.OutputType]

	imagesDir, imagesPrefix := path.Split(o.ImagePrefix)

	o.StorageDir = imagesDir

	// ensure playlistName
	if imagesPrefix == "" {
		imagesPrefix = fmt.Sprintf("%s-%s", identifier, time.Now().Format("2006-01-02T150405"))
	}

	// update config
	o.ImagePrefix = imagesPrefix

	if o.UploadConfig == nil {
		o.LocalDir = imagesDir
	} else {
		// Prepend the configuration base directory and the egress Id, and slug to prevent conflict if
		// there is more than one image output
		// os.ModeDir creates a directory with mode 000 when mapping the directory outside the container
		// Append a "/" to the path for consistency with the "UploadConfig == nil" case

		o.LocalDir = path.Join(TmpDir, p.Info.EgressId, o.Id) + "/"
	}

	// create local directories
	if o.LocalDir != "" {
		if err := os.MkdirAll(o.LocalDir, 0755); err != nil {
			return err
		}
	}

	return nil
}
func getMimeTypes(imageCodec livekit.ImageCodec) (types.MimeType, types.OutputType, error) {
	switch imageCodec {
	case livekit.ImageCodec_IC_DEFAULT, livekit.ImageCodec_IC_JPEG:
		return types.MimeTypeJPEG, types.OutputTypeJPEG, nil
	default:
		return "", "", errors.ErrNoCompatibleCodec
	}
}
