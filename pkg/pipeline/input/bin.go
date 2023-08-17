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
	audioBin *gst.Bin
	videoBin *gst.Bin

	audio *audioInput
	video *videoInput
}

func New(ctx context.Context, pipeline *gst.Pipeline, p *config.PipelineConfig) (*Bin, error) {
	ctx, span := tracer.Start(ctx, "Input.New")
	defer span.End()

	b := &Bin{
		audioBin: gst.NewBin("audio_input"),
		videoBin: gst.NewBin("video_input"),
	}

	// build input
	if p.AudioEnabled {
		if err := b.buildAudioInput(p); err != nil {
			return nil, err
		}
		if err := pipeline.Add(b.audioBin.Element); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}
	if p.VideoEnabled {
		if err := b.buildVideoInput(p); err != nil {
			return nil, err
		}
		if err := pipeline.Add(b.videoBin.Element); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}

	return b, nil
}

func (b *Bin) buildAudioInput(p *config.PipelineConfig) error {
	a := &audioInput{}

	// build input
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

	// build encoder
	if p.AudioTranscoding {
		if err := a.buildEncoder(p); err != nil {
			return err
		}
	}

	// add elements to bin
	if err := b.audioBin.AddMany(a.src...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := b.audioBin.AddMany(a.testSrc...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := b.audioBin.AddMany(a.mixer...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if a.encoder != nil {
		if err := b.audioBin.Add(a.encoder); err != nil {
			return errors.ErrGstPipelineError(err)
		}
	}

	b.audio = a
	return nil
}

func (b *Bin) buildVideoInput(p *config.PipelineConfig) error {
	v := &videoInput{}

	// build input
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

	// build encoder
	if p.VideoTranscoding {
		if err := v.buildEncoder(p); err != nil {
			return err
		}
	}

	// add elements to bin
	if err := b.videoBin.AddMany(v.src...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := b.videoBin.AddMany(v.testSrc...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if v.selector != nil {
		if err := b.videoBin.Add(v.selector); err != nil {
			return errors.ErrGstPipelineError(err)
		}
	}
	if err := b.videoBin.AddMany(v.encoder...); err != nil {
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
		if !b.audioBin.AddPad(audioPad.Pad) {
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
		if !b.videoBin.AddPad(videoPad.Pad) {
			err = errors.ErrGhostPadFailed
			return
		}
	}

	return
}

func (b *Bin) SendEOS() {
	if b.audioBin != nil {
		b.audioBin.SendEvent(gst.NewEOSEvent())
	}
	if b.videoBin != nil {
		b.videoBin.SendEvent(gst.NewEOSEvent())
	}
}
