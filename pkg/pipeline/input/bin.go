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
	lksdk "github.com/livekit/server-sdk-go"
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

	// set up callbacks
	if p.RequestType == types.RequestTypeParticipant {
		p.OnTrackAdded = func(track *config.TrackSource) {
			var err error
			if track.Kind == lksdk.TrackKindAudio {
				err = b.addAudioInput(p, track)
			} else {
				err = b.addVideoInput(p, track)
			}
			if err != nil {
				p.OnFailure(err)
			}
		}
		p.OnTrackRemoved = func(trackID string) {
			var err error
			if b.audio.trackID == trackID {
				err = b.removeAudioInput()
			} else if b.video.trackID == trackID {
				err = b.removeVideoInput()
			}
			if err != nil {
				p.OnFailure(err)
			}
		}
	}

	// build input
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

	// add bin to pipeline
	if err := pipeline.Add(b.bin.Element); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return b, nil
}

func (b *Bin) buildAudioInput(p *config.PipelineConfig) error {
	a := &audioInput{}
	a.mu.Lock()
	defer a.mu.Unlock()

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
		// build encoder
		if err := a.buildEncoder(p); err != nil {
			return err
		}
	}

	// add elements to bin
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
	v.mu.Lock()
	defer v.mu.Unlock()

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
	if err := b.bin.AddMany(v.src...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := b.bin.AddMany(v.testSrc...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if v.selector != nil {
		if err := b.bin.Add(v.selector); err != nil {
			return errors.ErrGstPipelineError(err)
		}
	}
	if err := b.bin.AddMany(v.encoder...); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	b.video = v
	return nil
}

func (b *Bin) addAudioInput(p *config.PipelineConfig, track *config.TrackSource) error {
	b.audio.mu.Lock()
	defer b.audio.mu.Unlock()

	// build appsrc
	if err := b.audio.buildAppSource(track); err != nil {
		return err
	}
	if err := b.audio.buildConverter(p); err != nil {
		return err
	}

	// add appsrc to bin
	if err := b.bin.AddMany(b.audio.src...); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	// link appsrc
	if err := b.audio.linkAppSrc(); err != nil {
		return err
	}

	b.bin.SyncStateWithParent()
	return nil
}

func (b *Bin) addVideoInput(p *config.PipelineConfig, track *config.TrackSource) error {
	b.video.mu.Lock()
	defer b.video.mu.Unlock()

	// build appsrc
	if err := b.video.buildAppSource(p, track); err != nil {
		return err
	}

	// add appsrc to bin
	if err := b.bin.AddMany(b.video.src...); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	// link appsrc
	if err := b.video.linkAppSrc(); err != nil {
		return err
	}

	b.bin.SyncStateWithParent()
	return nil
}

func (b *Bin) removeAudioInput() error {
	b.audio.mu.Lock()
	defer b.audio.mu.Unlock()

	return b.audio.removeAppSrc(b.bin)
}

func (b *Bin) removeVideoInput() error {
	b.video.mu.Lock()
	defer b.video.mu.Unlock()

	return b.video.removeAppSrc(b.bin)
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
