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

package input

import (
	"context"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/tracer"
)

type Bin struct {
	bin *gst.Bin

	audio *audioInput
	video *videoInput
}

func New(ctx context.Context, pipeline *gst.Pipeline, p *config.PipelineConfig) (*Bin, error) {
	ctx, span := tracer.Start(ctx, "Input.New")
	defer span.End()

	b := &Bin{
		bin: gst.NewBin("input"),
	}
	if p.AudioEnabled {
		if err := b.buildAudioInput(p); err != nil {
			return nil, err
		}
	}
	if p.VideoEnabled {
		if err := b.buildVideoInput(p); err != nil {
			return nil, err
		}
	}
	if err := pipeline.Add(b.bin.Element); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return b, nil
}

func (b *Bin) buildAudioInput(p *config.PipelineConfig) error {
	a := &audioInput{}

	switch p.SourceType {
	case types.SourceTypeSDK:
		if err := a.buildSDKInput(p); err != nil {
			return err
		}

	case types.SourceTypeWeb:
		if err := a.buildWebInput(p); err != nil {
			return err
		}
	}

	if p.AudioTranscoding {
		if err := a.buildEncoder(p); err != nil {
			return err
		}
	}

	// Add elements to bin
	if err := b.bin.AddMany(a.src...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := b.bin.AddMany(a.testSrc...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := b.bin.AddMany(a.mixer...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if a.encoder != nil {
		if err := b.bin.Add(a.encoder); err != nil {
			return errors.ErrGstPipelineError(err)
		}
	}

	b.audio = a
	return nil
}

func (b *Bin) buildVideoInput(p *config.PipelineConfig) error {
	v := &videoInput{}

	switch p.SourceType {
	case types.SourceTypeSDK:
		if err := v.buildSDKInput(p); err != nil {
			return err
		}

	case types.SourceTypeWeb:
		if err := v.buildWebInput(p); err != nil {
			return err
		}
	}

	if p.VideoTranscoding {
		if err := v.buildEncoder(p); err != nil {
			return err
		}
	}

	// Add elements to bin
	if err := b.bin.AddMany(v.src...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := b.bin.AddMany(v.encoder...); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	b.video = v
	return nil
}

func (b *Bin) Link() (audioPad, videoPad *gst.GhostPad, err error) {
	// link audio elements
	if b.audio != nil {
		audioPad, err = b.audio.link()
		if err != nil {
			return
		}
		if !b.bin.AddPad(audioPad.Pad) {
			err = errors.ErrGhostPadFailed
			return
		}
	}

	// link video elements
	if b.video != nil {
		videoPad, err = b.video.link()
		if err != nil {
			return
		}
		if !b.bin.AddPad(videoPad.Pad) {
			err = errors.ErrGhostPadFailed
			return
		}
	}

	return
}
