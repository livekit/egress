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
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

type OutputConfig interface {
	GetOutputType() types.OutputType
}

type outputConfig struct {
	types.OutputType
}

func (o outputConfig) GetOutputType() types.OutputType {
	return o.OutputType
}

func (p *PipelineConfig) updateEncodedOutputs(req egress.EncodedOutput) error {
	files := req.GetFileOutputs()
	streams := req.GetStreamOutputs()
	segments := req.GetSegmentOutputs()
	images := req.GetImageOutputs()

	// file output
	var file *livekit.EncodedFileOutput
	switch len(files) {
	case 0:
		if r, ok := req.(egress.EncodedOutputDeprecated); ok {
			file = r.GetFile()
		}
	case 1:
		file = files[0]
	default:
		return errors.ErrInvalidInput("multiple file outputs")
	}
	if file != nil {
		conf, err := p.getEncodedFileConfig(file)
		if err != nil {
			return err
		}

		p.Outputs[types.EgressTypeFile] = []OutputConfig{conf}
		p.OutputCount++
		p.FinalizationRequired = true
		if p.VideoEnabled {
			p.VideoEncoding = true
		}

		p.Info.FileResults = []*livekit.FileInfo{conf.FileInfo}
		if len(streams)+len(segments)+len(images) == 0 {
			p.Info.Result = &livekit.EgressInfo_File{File: conf.FileInfo}
			return nil
		}
	}

	// stream output
	var stream *livekit.StreamOutput
	switch len(streams) {
	case 0:
		if r, ok := req.(egress.EncodedOutputDeprecated); ok {
			stream = r.GetStream()
		}
	case 1:
		stream = streams[0]
	default:
		return errors.ErrInvalidInput("multiple stream outputs")
	}
	if stream != nil {
		conf, err := p.getStreamConfig(types.OutputTypeRTMP, stream.Urls)
		if err != nil {
			return err
		}

		p.Outputs[types.EgressTypeStream] = []OutputConfig{conf}
		p.OutputCount += len(stream.Urls)
		if p.VideoEnabled {
			p.VideoEncoding = true
		}

		streamInfoList := make([]*livekit.StreamInfo, 0, len(conf.StreamInfo))
		for _, info := range conf.StreamInfo {
			streamInfoList = append(streamInfoList, info)
		}
		p.Info.StreamResults = streamInfoList
		if len(files)+len(segments)+len(images) == 0 {
			// empty stream output only valid in combination with other outputs
			if len(stream.Urls) == 0 {
				return errors.ErrInvalidInput("stream url")
			}

			p.Info.Result = &livekit.EgressInfo_Stream{Stream: &livekit.StreamInfoList{Info: streamInfoList}}
			return nil
		}
	}

	// segment output
	var segment *livekit.SegmentedFileOutput
	switch len(segments) {
	case 0:
		if r, ok := req.(egress.EncodedOutputDeprecated); ok {
			segment = r.GetSegments()
		}
	case 1:
		segment = segments[0]
	default:
		return errors.ErrInvalidInput("multiple segmented file outputs")
	}
	if segment != nil {
		conf, err := p.getSegmentConfig(segment)
		if err != nil {
			return err
		}

		p.Outputs[types.EgressTypeSegments] = []OutputConfig{conf}
		p.OutputCount++
		p.FinalizationRequired = true
		if p.VideoEnabled {
			p.VideoEncoding = true
		}

		p.Info.SegmentResults = []*livekit.SegmentsInfo{conf.SegmentsInfo}
		if len(streams)+len(files)+len(images) == 0 {
			p.Info.Result = &livekit.EgressInfo_Segments{Segments: conf.SegmentsInfo}
			return nil
		}
	}

	if segmentConf := p.Outputs[types.EgressTypeSegments]; segmentConf != nil {
		p.KeyFrameInterval = 0
	} else if p.KeyFrameInterval == 0 && p.Outputs[types.EgressTypeStream] != nil {
		// default 4s for streams
		p.KeyFrameInterval = 4
	}

	err := p.updateImageOutputs(images)
	if err != nil {
		return err
	}

	if p.OutputCount == 0 {
		return errors.ErrInvalidInput("output")
	}

	return nil
}

func (p *PipelineConfig) updateDirectOutput(req *livekit.TrackEgressRequest) error {
	switch o := req.Output.(type) {
	case *livekit.TrackEgressRequest_File:
		conf, err := p.getDirectFileConfig(o.File)
		if err != nil {
			return err
		}

		p.Info.FileResults = []*livekit.FileInfo{conf.FileInfo}
		p.Info.Result = &livekit.EgressInfo_File{File: conf.FileInfo}

		p.Outputs[types.EgressTypeFile] = []OutputConfig{conf}
		p.OutputCount = 1
		p.FinalizationRequired = true

	case *livekit.TrackEgressRequest_WebsocketUrl:
		conf, err := p.getStreamConfig(types.OutputTypeRaw, []string{o.WebsocketUrl})
		if err != nil {
			return err
		}

		streamInfoList := make([]*livekit.StreamInfo, 0, len(conf.StreamInfo))
		for _, info := range conf.StreamInfo {
			streamInfoList = append(streamInfoList, info)
		}
		p.Info.StreamResults = streamInfoList
		p.Info.Result = &livekit.EgressInfo_Stream{Stream: &livekit.StreamInfoList{Info: streamInfoList}}

		p.Outputs[types.EgressTypeWebsocket] = []OutputConfig{conf}
		p.OutputCount = 1

	default:
		return errors.ErrInvalidInput("output")
	}

	return nil
}

func (p *PipelineConfig) updateImageOutputs(images []*livekit.ImageOutput) error {

	if len(images) > 0 && !p.VideoEnabled {
		return errors.ErrInvalidInput("audio_only")
	}

	for _, img := range images {
		conf, err := p.getImageConfig(img)
		if err != nil {
			return err
		}

		p.Outputs[types.EgressTypeImages] = append(p.Outputs[types.EgressTypeImages], conf)
		p.OutputCount++
		p.FinalizationRequired = true

		p.Info.ImageResults = append(p.Info.ImageResults, conf.ImagesInfo)
	}

	return nil
}

func redactEncodedOutputs(out egress.EncodedOutput) {
	if files := out.GetFileOutputs(); len(files) == 1 {
		redactUpload(files[0])
	}
	if streams := out.GetStreamOutputs(); len(streams) == 1 {
		redactStreamKeys(streams[0])
	}
	if segments := out.GetSegmentOutputs(); len(segments) == 1 {
		redactUpload(segments[0])
	}
	if o, ok := out.(egress.EncodedOutputDeprecated); ok {
		if file := o.GetFile(); file != nil {
			redactUpload(file)
		} else if stream := o.GetStream(); stream != nil {
			redactStreamKeys(stream)
		} else if segment := o.GetSegments(); segment != nil {
			redactUpload(segment)
		}
	}
}

func redactStreamKeys(stream *livekit.StreamOutput) {
	for i, url := range stream.Urls {
		if redacted, ok := utils.RedactStreamKey(url); ok {
			stream.Urls[i] = redacted
		}
	}
}
