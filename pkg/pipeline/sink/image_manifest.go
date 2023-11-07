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
	"encoding/json"
	"os"
	"time"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/types"
)

type ImageManifest struct {
	Manifest `json:",inline"`

	Images []*Image `json:"images"`
}

type Image struct {
	Name      string    `json:"name"`
	Timestamp time.Time `json:"timestamp"`
	Size      int64     `json:"size"`
}

func createImageManifest(p *config.PipelineConfig) *ImageManifest {
	return &ImageManifest{
		Manifest: initManifest(p),
	}
}

func (m *ImageManifest) imageCreated(filename string, ts time.Time, size int64) {
	m.Images = append(m.Images, &Image{
		Name:      filename,
		Timestamp: ts,
		Size:      size,
	})
}

func (m *ImageManifest) updateManifest(u uploader.Uploader, localFilepath, storageFilepath string) error {
	manifest, err := os.Create(localFilepath)
	if err != nil {
		return err
	}

	b, err := json.Marshal(m)
	if err != nil {
		return err
	}

	_, err = manifest.Write(b)
	if err != nil {
		return err
	}

	err = manifest.Close()
	if err != nil {
		return err
	}

	_, _, err = u.Upload(localFilepath, storageFilepath, types.OutputTypeJSON, false, "image_manifest")

	return err
}
