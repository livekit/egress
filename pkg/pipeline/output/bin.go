package output

import (
	"context"
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

type Bin struct {
	bin *gst.Bin

	audioTee *gst.Element
	videoTee *gst.Element

	outputs map[types.EgressType]output
}

type output interface {
	Link(audioTee, videoTee *gst.Element) error
}

func New(ctx context.Context, pipeline *gst.Pipeline, p *config.PipelineConfig) (*Bin, error) {
	ctx, span := tracer.Start(ctx, "Output.New")
	defer span.End()

	b := &Bin{
		bin:     gst.NewBin("output"),
		outputs: make(map[types.EgressType]output),
	}

	for egressType, conf := range p.Outputs {
		logger.Infow("output config", "conf", fmt.Sprintf("%+v", conf))
		switch egressType {
		case types.EgressTypeFile:
			o, err := buildFileOutput(b.bin, conf)
			if err != nil {
				return nil, err
			}
			b.outputs[conf.EgressType] = o

		case types.EgressTypeSegments:
			o, err := buildSegmentOutput(b.bin, conf)
			if err != nil {
				return nil, err
			}
			b.outputs[conf.EgressType] = o

		case types.EgressTypeStream:
			o, err := buildStreamOutput(b.bin, conf)
			if err != nil {
				return nil, err
			}
			b.outputs[conf.EgressType] = o

		case types.EgressTypeWebsocket:
			o, err := buildWebsocketOutput(b.bin, conf)
			if err != nil {
				return nil, err
			}
			b.outputs[conf.EgressType] = o

		default:
			return nil, errors.ErrInvalidInput("egress type")
		}
	}

	var err error
	if p.AudioEnabled {
		b.audioTee, err = gst.NewElement("tee")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = b.bin.Add(b.audioTee); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		audioPad := gst.NewGhostPad("audio", b.audioTee.GetStaticPad("sink"))
		if !b.bin.AddPad(audioPad.Pad) {
			return nil, errors.ErrGhostPadFailed
		}
	}

	if p.VideoEnabled {
		b.videoTee, err = gst.NewElement("tee")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = b.bin.Add(b.videoTee); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		videoPad := gst.NewGhostPad("video", b.videoTee.GetStaticPad("sink"))
		if !b.bin.AddPad(videoPad.Pad) {
			return nil, errors.ErrGhostPadFailed
		}
	}

	if err = pipeline.Add(b.bin.Element); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return b, nil
}

func (b *Bin) Link(audioSrc, videoSrc *gst.GhostPad) error {
	for _, out := range b.outputs {
		if err := out.Link(b.audioTee, b.videoTee); err != nil {
			return err
		}
	}

	if b.audioTee != nil {
		if err := builder.LinkPads("audio input", audioSrc, "audio output", b.bin.GetStaticPad("audio")); err != nil {
			return err
		}
	}

	if b.videoTee != nil {
		if err := builder.LinkPads("video input", videoSrc, "video output", b.bin.GetStaticPad("video")); err != nil {
			return err
		}
	}

	return nil
}

func (b *Bin) AddStream(url string) error {
	o := b.outputs[types.EgressTypeStream]
	if o == nil {
		// TODO: add StreamOutput to running pipeline
		return errors.ErrNotSupported("add stream")
	}

	return o.(*StreamOutput).AddSink(b.bin, url)
}

func (b *Bin) GetStreamUrl(name string) (string, error) {
	o := b.outputs[types.EgressTypeStream]
	if o == nil {
		return "", errors.ErrStreamNotFound
	}

	return o.(*StreamOutput).GetUrl(name)
}

func (b *Bin) RemoveStream(url string) error {
	o := b.outputs[types.EgressTypeStream]
	if o == nil {
		return errors.ErrStreamNotFound
	}

	return o.(*StreamOutput).RemoveSink(b.bin, url)
}
