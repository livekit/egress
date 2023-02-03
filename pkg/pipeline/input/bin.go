package input

import (
	"context"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/tracer"
)

type Bin struct {
	bin *gst.Bin

	audio *AudioInput
	video *VideoInput
}

func New(ctx context.Context, pipeline *gst.Pipeline, p *config.PipelineConfig) (*Bin, error) {
	ctx, span := tracer.Start(ctx, "Input.New")
	defer span.End()

	var err error
	b := &Bin{
		bin: gst.NewBin("bin"),
	}

	if p.AudioEnabled {
		b.audio, err = newAudioInput(b.bin, p)
		if err != nil {
			return nil, err
		}
	}

	if p.VideoEnabled {
		b.video, err = newVideoInput(b.bin, p)
		if err != nil {
			return nil, err
		}
	}

	if err := pipeline.Add(b.bin.Element); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return b, nil
}

func (b *Bin) Link() (audioPad, videoPad *gst.GhostPad, err error) {
	// link audio elements
	if b.audio != nil {
		audioPad, err = b.audio.Link()
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
		videoPad, err = b.video.Link()
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
