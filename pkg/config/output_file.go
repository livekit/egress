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
	"strings"
	"time"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

type FileConfig struct {
	outputConfig

	FileInfo        *livekit.FileInfo
	LocalFilepath   string
	StorageFilepath string

	DisableManifest bool
	UploadConfig    UploadConfig
}

func (p *PipelineConfig) GetFileConfig() *FileConfig {
	o, ok := p.Outputs[types.EgressTypeFile]
	if !ok || len(o) == 0 {
		return nil
	}
	return o[0].(*FileConfig)
}

func (p *PipelineConfig) getEncodedFileConfig(file *livekit.EncodedFileOutput) (*FileConfig, error) {
	var outputType types.OutputType

	switch file.FileType {
	case livekit.EncodedFileType_DEFAULT_FILETYPE:
		outputType = types.OutputTypeUnknownFile
	case livekit.EncodedFileType_MP4:
		outputType = types.OutputTypeMP4
	case livekit.EncodedFileType_OGG:
		outputType = types.OutputTypeOGG
	}

	return p.getFileConfig(outputType, file)
}

func (p *PipelineConfig) getDirectFileConfig(file *livekit.DirectFileOutput) (*FileConfig, error) {
	return p.getFileConfig(types.OutputTypeUnknownFile, file)
}

type fileRequest interface {
	GetFilepath() string
	GetDisableManifest() bool
	uploadRequest
}

func (p *PipelineConfig) getFileConfig(outputType types.OutputType, req fileRequest) (*FileConfig, error) {
	conf := &FileConfig{
		outputConfig:    outputConfig{OutputType: outputType},
		FileInfo:        &livekit.FileInfo{},
		StorageFilepath: clean(req.GetFilepath()),
		DisableManifest: req.GetDisableManifest(),
		UploadConfig:    p.getUploadConfig(req),
	}

	// filename
	identifier, replacements := p.getFilenameInfo()
	if conf.OutputType != types.OutputTypeUnknownFile {
		err := conf.updateFilepath(p, identifier, replacements)
		if err != nil {
			return nil, err
		}
	} else {
		conf.StorageFilepath = stringReplace(conf.StorageFilepath, replacements)
	}

	return conf, nil
}

func (p *PipelineConfig) getFilenameInfo() (string, map[string]string) {
	now := time.Now()
	utc := fmt.Sprintf("%s%03d", now.Format("20060102150405"), now.UnixMilli()%1000)
	if p.Info.RoomName != "" {
		return p.Info.RoomName, map[string]string{
			"{room_name}": p.Info.RoomName,
			"{room_id}":   p.Info.RoomId,
			"{time}":      now.Format("2006-01-02T150405"),
			"{utc}":       utc,
		}
	}

	return "web", map[string]string{
		"{time}": now.Format("2006-01-02T150405"),
		"{utc}":  utc,
	}
}

func (o *FileConfig) updateFilepath(p *PipelineConfig, identifier string, replacements map[string]string) error {
	o.StorageFilepath = stringReplace(o.StorageFilepath, replacements)

	// get file extension
	ext := types.FileExtensionForOutputType[o.OutputType]

	if o.StorageFilepath == "" || strings.HasSuffix(o.StorageFilepath, "/") {
		// generate filepath
		o.StorageFilepath = fmt.Sprintf("%s%s-%s%s", o.StorageFilepath, identifier, time.Now().Format("2006-01-02T150405"), ext)
	} else if !strings.HasSuffix(o.StorageFilepath, string(ext)) {
		// check for existing (incorrect) extension
		if extIdx := strings.LastIndex(o.StorageFilepath, "."); extIdx > -1 {
			existingExt := types.FileExtension(o.StorageFilepath[extIdx:])
			if _, ok := types.FileExtensions[existingExt]; ok {
				o.StorageFilepath = o.StorageFilepath[:extIdx]
			}
		}

		// add file extension
		o.StorageFilepath = o.StorageFilepath + string(ext)
	}

	// update filename
	o.FileInfo.Filename = o.StorageFilepath

	// get local filepath
	dir, filename := path.Split(o.StorageFilepath)
	if o.UploadConfig == nil {
		if dir != "" {
			// create local directory
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
		}
		// write directly to requested location
		o.LocalFilepath = o.StorageFilepath
	} else {
		// prepend the configuration base directory and the egress Id
		tempDir := path.Join(TmpDir, p.Info.EgressId)

		// create temporary directory
		if err := os.MkdirAll(tempDir, 0755); err != nil {
			return err
		}

		// write to tmp dir
		o.LocalFilepath = path.Join(tempDir, filename)
	}

	return nil
}

func clean(filepath string) string {
	hasEndingSlash := strings.HasSuffix(filepath, "/")
	filepath = path.Clean(filepath)
	for strings.HasPrefix(filepath, "../") {
		filepath = filepath[3:]
	}
	if filepath == "" || filepath == "." || filepath == ".." {
		return ""
	}
	if hasEndingSlash {
		return filepath + "/"
	}
	return filepath
}
