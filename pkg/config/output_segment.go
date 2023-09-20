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

type SegmentConfig struct {
	outputConfig

	SegmentsInfo     *livekit.SegmentsInfo
	LocalDir         string
	StorageDir       string
	PlaylistFilename string
	SegmentPrefix    string
	SegmentSuffix    livekit.SegmentedFileSuffix
	SegmentDuration  int

	DisableManifest bool
	UploadConfig    UploadConfig
}

func (p *PipelineConfig) GetSegmentConfig() *SegmentConfig {
	o, ok := p.Outputs[types.EgressTypeSegments]
	if !ok || len(o) == 0 {
		return nil
	}
	return o[0].(*SegmentConfig)
}

// segments should always be added last, so we can check keyframe interval from file/stream
func (p *PipelineConfig) getSegmentConfig(segments *livekit.SegmentedFileOutput) (*SegmentConfig, error) {
	conf := &SegmentConfig{
		SegmentsInfo:     &livekit.SegmentsInfo{},
		SegmentPrefix:    clean(segments.FilenamePrefix),
		SegmentSuffix:    segments.FilenameSuffix,
		PlaylistFilename: clean(segments.PlaylistName),
		SegmentDuration:  int(segments.SegmentDuration),
		DisableManifest:  segments.DisableManifest,
		UploadConfig:     p.getUploadConfig(segments),
	}

	if conf.SegmentDuration == 0 {
		if p.KeyFrameInterval >= 1 {
			conf.SegmentDuration = int(p.KeyFrameInterval)
		} else {
			conf.SegmentDuration = 4
		}
	}

	if p.KeyFrameInterval == 0 {
		// The splitMuxSink should request key frames from the encoder at expected frame boundaries.
		// Set the key frame interval to twice the segment duration as a failsafe
		p.KeyFrameInterval = 2 * float64(conf.SegmentDuration)
	} else if p.KeyFrameInterval < float64(conf.SegmentDuration) && p.KeyFrameInterval >= 1 {
		conf.SegmentDuration = int(p.KeyFrameInterval)
	}

	switch segments.Protocol {
	case livekit.SegmentedFileProtocol_DEFAULT_SEGMENTED_FILE_PROTOCOL,
		livekit.SegmentedFileProtocol_HLS_PROTOCOL:
		conf.OutputType = types.OutputTypeHLS
	}

	// filename
	err := conf.updatePrefixAndPlaylist(p)
	if err != nil {
		return nil, err
	}

	return conf, nil
}

func (o *SegmentConfig) updatePrefixAndPlaylist(p *PipelineConfig) error {
	identifier, replacements := p.getFilenameInfo()

	o.SegmentPrefix = stringReplace(o.SegmentPrefix, replacements)
	o.PlaylistFilename = stringReplace(o.PlaylistFilename, replacements)

	ext := types.FileExtensionForOutputType[o.OutputType]

	playlistDir, playlistName := path.Split(o.PlaylistFilename)
	fileDir, filePrefix := path.Split(o.SegmentPrefix)

	// remove extension from playlist name
	if extIdx := strings.LastIndex(playlistName, "."); extIdx > -1 {
		existingExt := types.FileExtension(playlistName[extIdx:])
		if _, ok := types.FileExtensions[existingExt]; ok {
			playlistName = playlistName[:extIdx]
		}
		playlistName = playlistName[:extIdx]
	}

	// only keep fileDir if it is a subdirectory of playlistDir
	if fileDir != "" {
		if playlistDir == fileDir {
			fileDir = ""
		} else if playlistDir == "" {
			playlistDir = fileDir
			fileDir = ""
		}
	}
	o.StorageDir = playlistDir

	// ensure playlistName
	if playlistName == "" {
		if filePrefix != "" {
			playlistName = filePrefix
		} else {
			playlistName = fmt.Sprintf("%s-%s", identifier, time.Now().Format("2006-01-02T150405"))
		}
	}

	// ensure filePrefix
	if filePrefix == "" {
		filePrefix = playlistName
	}

	// update config
	o.StorageDir = playlistDir
	o.PlaylistFilename = fmt.Sprintf("%s%s", playlistName, ext)
	o.SegmentPrefix = fmt.Sprintf("%s%s", fileDir, filePrefix)

	if o.UploadConfig == nil {
		o.LocalDir = playlistDir
	} else {
		// Prepend the configuration base directory and the egress Id
		// os.ModeDir creates a directory with mode 000 when mapping the directory outside the container
		// Append a "/" to the path for consistency with the "UploadConfig == nil" case
		o.LocalDir = path.Join(TmpDir, p.Info.EgressId) + "/"
	}

	// create local directories
	if fileDir != "" {
		if err := os.MkdirAll(path.Join(o.LocalDir, fileDir), 0755); err != nil {
			return err
		}
	} else if o.LocalDir != "" {
		if err := os.MkdirAll(o.LocalDir, 0755); err != nil {
			return err
		}
	}

	o.SegmentsInfo.PlaylistName = path.Join(o.StorageDir, o.PlaylistFilename)
	return nil
}
