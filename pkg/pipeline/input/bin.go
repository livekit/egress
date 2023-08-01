package input

import (
	"context"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
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
		p.OnTrackAdded = func(track lksdk.TrackPublication) {
			var err error
			if track.Kind() == lksdk.TrackKindAudio {
				err = b.addAudioInput(p)
			} else {
				err = b.addVideoInput(p)
			}
			if err != nil {
				p.OnFailure(err)
			}
		}
		p.OnTrackRemoved = func(track lksdk.TrackPublication) {
			if track.Kind() == lksdk.TrackKindAudio {
				b.removeAudioInput()
			} else {
				b.removeVideoInput()
			}
		}
	}

	// build audio input
	if p.AudioEnabled {
		if err := b.buildAudioInput(p); err != nil {
			return nil, err
		}
	}

	// build video input
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

	switch p.SourceType {
	case types.SourceTypeSDK:
		// build sdk input
		if err := a.buildSDKInput(p); err != nil {
			return err
		}

	case types.SourceTypeWeb:
		// build web input
		if err := a.buildWebInput(p); err != nil {
			return err
		}
	}

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

	switch p.SourceType {
	case types.SourceTypeSDK:
		// build sdk input
		if err := v.buildSDKInput(p); err != nil {
			return err
		}

	case types.SourceTypeWeb:
		// build web input
		if err := v.buildWebInput(p); err != nil {
			return err
		}
	}

	if p.VideoTranscoding {
		// build encoder
		if err := v.buildEncoder(p); err != nil {
			return err
		}
	}

	// add elements to bin
	if err := b.bin.AddMany(v.src...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := b.bin.AddMany(v.encoder...); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	b.video = v
	return nil
}

func (b *Bin) addAudioInput(p *config.PipelineConfig) error {
	// build appsrc
	if err := b.audio.buildAppSource(p); err != nil {
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

	logger.Infow("sync audio state")
	b.bin.SyncStateWithParent()
	logger.Infow("sync state done")
	return nil
}

func (b *Bin) addVideoInput(p *config.PipelineConfig) error {
	return errors.New("unimplemented")
}

func (b *Bin) removeAudioInput() {
	b.audio.unlinkAppSrc(b.bin)
}

func (b *Bin) removeVideoInput() {
	return
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
