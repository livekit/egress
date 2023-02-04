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

	for _, out := range p.Outputs {
		if err := b.buildOutput(p, out); err != nil {
			return nil, err
		}
	}

	var err error
	if p.AudioEnabled {
		// create audio ghost pad
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
		// create video ghost pad
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

func (b *Bin) buildOutput(p *config.PipelineConfig, out *config.OutputConfig) error {
	switch out.EgressType {
	case types.EgressTypeFile:
		o, err := b.buildFileOutput(p, out)
		if err != nil {
			return err
		}
		b.outputs[out.EgressType] = o

	case types.EgressTypeSegments:
		o, err := b.buildSegmentOutput(p, out)
		if err != nil {
			return err
		}
		b.outputs[out.EgressType] = o

	case types.EgressTypeStream:
		o, err := b.buildStreamOutput(p, out)
		if err != nil {
			return err
		}
		b.outputs[out.EgressType] = o

	case types.EgressTypeWebsocket:
		o, err := b.buildWebsocketOutput(p)
		if err != nil {
			return err
		}
		b.outputs[out.EgressType] = o

	default:
		return errors.ErrInvalidInput("egress type")
	}

	return nil
}

func (b *Bin) buildQueues(p *config.PipelineConfig) (audioQueue, videoQueue *gst.Element, err error) {
	if p.AudioEnabled {
		audioQueue, err = builder.BuildQueueWithLatency(p.Latency, true)
		if err != nil {
			return
		}
		if err = b.bin.Add(audioQueue); err != nil {
			return
		}
	}

	if p.VideoEnabled {
		videoQueue, err = builder.BuildQueueWithLatency(p.Latency, true)
		if err != nil {
			return
		}
		if err = b.bin.Add(videoQueue); err != nil {
			return
		}
	}

	return
}

func (b *Bin) Link(audioSrc, videoSrc *gst.GhostPad) error {
	// link audio to audio tee
	if b.audioTee != nil {
		if err := builder.LinkPads("audio input", audioSrc, "audio output", b.bin.GetStaticPad("audio")); err != nil {
			return err
		}
	}

	// link video to video tee
	if b.videoTee != nil {
		if err := builder.LinkPads("video input", videoSrc, "video output", b.bin.GetStaticPad("video")); err != nil {
			return err
		}
	}

	// link tees to outputs
	for _, out := range b.outputs {
		if err := out.Link(b.audioTee, b.videoTee); err != nil {
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

func (b *Bin) SetWebsocketSink(writer *sink.WebsocketSink) error {
	o := b.outputs[types.EgressTypeWebsocket]
	if o == nil {
		return errors.ErrGstPipelineError(errors.New("missing websocket output"))
	}

	o.(*WebsocketOutput).SetSink(writer)
	return nil
}
