package output

import (
	"context"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/pipeline/sink"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/psrpc"
)

type Bin struct {
	bin *gst.Bin

	audioTee *gst.Element
	videoTee *gst.Element

	outputs map[types.EgressType]output
}

type output interface {
	CreateGhostPads() (audioPad, videoPad *gst.GhostPad)
	LinkTees(audioTee, videoTee *gst.Element) error
	Link() error
}

func New(ctx context.Context, pipeline *gst.Pipeline, p *config.PipelineConfig) (*Bin, error) {
	ctx, span := tracer.Start(ctx, "Output.New")
	defer span.End()

	b := &Bin{
		bin:     gst.NewBin("output"),
		outputs: make(map[types.EgressType]output),
	}

	for egressType := range p.Outputs {
		if err := b.buildOutput(p, egressType); err != nil {
			return nil, err
		}
	}

	// create ghost pads
	var audioPad, videoPad *gst.GhostPad
	if len(b.outputs) == 1 {
		for _, out := range b.outputs {
			audioPad, videoPad = out.CreateGhostPads()
		}
	} else {
		var err error
		if p.AudioEnabled {
			// create audio tee
			b.audioTee, err = gst.NewElement("tee")
			if err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}
			if err = b.bin.Add(b.audioTee); err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}
			audioPad = gst.NewGhostPad("audio", b.audioTee.GetStaticPad("sink"))
		}

		if p.VideoEnabled {
			// create video tee
			b.videoTee, err = gst.NewElement("tee")
			if err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}
			if err = b.bin.Add(b.videoTee); err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}
			videoPad = gst.NewGhostPad("video", b.videoTee.GetStaticPad("sink"))
		}
	}

	// add ghost pads
	if audioPad != nil && !b.bin.AddPad(audioPad.Pad) {
		return nil, errors.ErrGhostPadFailed
	}
	if videoPad != nil && !b.bin.AddPad(videoPad.Pad) {
		return nil, errors.ErrGhostPadFailed
	}

	if err := pipeline.Add(b.bin.Element); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return b, nil
}

func (b *Bin) buildOutput(p *config.PipelineConfig, egressType types.EgressType) error {
	switch egressType {
	case types.EgressTypeFile:
		o, err := b.buildFileOutput(p)
		if err != nil {
			return err
		}
		b.outputs[egressType] = o

	case types.EgressTypeSegments:
		o, err := b.buildSegmentOutput(p)
		if err != nil {
			return err
		}
		b.outputs[egressType] = o

	case types.EgressTypeStream:
		o, err := b.buildStreamOutput(p)
		if err != nil {
			return err
		}
		b.outputs[egressType] = o

	case types.EgressTypeWebsocket:
		o, err := b.buildWebsocketOutput(p)
		if err != nil {
			return err
		}
		b.outputs[egressType] = o

	default:
		return errors.ErrInvalidInput("egress type")
	}

	return nil
}

func (b *Bin) Link(audioSrc, videoSrc *gst.GhostPad) error {
	if audioSrc != nil {
		if err := builder.LinkPads(
			"audio src", audioSrc,
			"audio output", b.bin.GetStaticPad("audio"),
		); err != nil {
			return err
		}
	}
	if videoSrc != nil {
		if err := builder.LinkPads(
			"video src", videoSrc,
			"video output", b.bin.GetStaticPad("video"),
		); err != nil {
			return err
		}
	}

	if len(b.outputs) == 1 {
		for _, out := range b.outputs {
			if err := out.Link(); err != nil {
				return err
			}
		}
	} else {
		// link tees to outputs
		for _, out := range b.outputs {
			if err := out.LinkTees(b.audioTee, b.videoTee); err != nil {
				return err
			}
			if err := out.Link(); err != nil {
				return err
			}
		}
	}

	return nil
}

func (b *Bin) AddStream(url string) error {
	o := b.outputs[types.EgressTypeStream]
	if o == nil {
		return errors.ErrNonStreamingPipeline
	}

	return o.(*StreamOutput).AddSink(b.bin, url)
}

func (b *Bin) GetStreamUrl(name string) (string, error) {
	o := b.outputs[types.EgressTypeStream]
	if o == nil {
		return "", errors.ErrStreamNotFound(name)
	}

	return o.(*StreamOutput).GetUrl(name)
}

func (b *Bin) RemoveStream(url string) error {
	o := b.outputs[types.EgressTypeStream]
	if o == nil {
		return errors.ErrStreamNotFound(url)
	}

	return o.(*StreamOutput).RemoveSink(b.bin, url)
}

func (b *Bin) SetWebsocketSink(writer *sink.WebsocketSink) error {
	o := b.outputs[types.EgressTypeWebsocket]
	if o == nil {
		return psrpc.NewErrorf(psrpc.Internal, "missing websocket output")
	}

	o.(*WebsocketOutput).SetSink(writer)
	return nil
}
