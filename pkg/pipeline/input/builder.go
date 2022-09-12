package input

import (
	"context"
	"fmt"
	"time"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/protocol/tracer"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/egress/pkg/pipeline/source"
)

func Build(ctx context.Context, conf *config.Config, p *params.Params) (*Bin, error) {
	ctx, span := tracer.Start(ctx, "Input.Build")
	defer span.End()

	// source
	var src source.Source
	var err error
	if p.IsWebSource {
		src, err = source.NewWebSource(ctx, conf, p)
		<-p.GstReady
	} else {
		src, err = source.NewSDKSource(ctx, p)
	}
	if err != nil {
		return nil, err
	}

	b := &Bin{
		bin:    gst.NewBin("input"),
		Source: src,
	}

	// audio elements
	err = b.buildAudioElements(p)
	if err != nil {
		return nil, err
	}

	// video elements
	err = b.buildVideoElements(p)
	if err != nil {
		return nil, err
	}

	// queue
	b.multiQueue, err = gst.NewElement("multiqueue")
	if err != nil {
		return nil, err
	}
	if err = b.bin.Add(b.multiQueue); err != nil {
		return nil, err
	}

	// mux
	err = b.buildMux(p)
	if err != nil {
		return nil, err
	}

	// HLS has no output bin
	if p.OutputType == params.OutputTypeHLS {
		return b, nil
	}

	// create ghost pad
	var ghostPad *gst.GhostPad
	if b.mux != nil {
		ghostPad = gst.NewGhostPad("src", b.mux.GetStaticPad("src"))
	} else if len(b.audioElements) != 0 {
		b.audioPad = b.multiQueue.GetRequestPad("sink_%u")
		ghostPad = gst.NewGhostPad("src", b.multiQueue.GetStaticPad("src_0"))
	} else if len(b.videoElements) != 0 {
		b.videoPad = b.multiQueue.GetRequestPad("sink_%u")
		ghostPad = gst.NewGhostPad("src", b.multiQueue.GetStaticPad("src_0"))
	}

	if !b.bin.AddPad(ghostPad.Pad) {
		return nil, errors.ErrGhostPadFailed
	}

	return b, nil
}

func (b *Bin) buildMux(p *params.Params) error {
	var err error
	switch p.OutputType {
	case params.OutputTypeRaw:
		return nil

	case params.OutputTypeOGG:
		b.mux, err = gst.NewElement("oggmux")

	case params.OutputTypeIVF:
		b.mux, err = gst.NewElement("avmux_ivf")

	case params.OutputTypeMP4:
		b.mux, err = gst.NewElement("mp4mux")
		if err != nil {
			return err
		}
		err = b.mux.SetProperty("faststart", true)

	case params.OutputTypeTS:
		b.mux, err = gst.NewElement("mpegtsmux")
		// TODO: filename needs to contain %d

	case params.OutputTypeWebM:
		b.mux, err = gst.NewElement("webmmux")

	case params.OutputTypeRTMP:
		b.mux, err = gst.NewElement("flvmux")
		if err != nil {
			return err
		}
		err = b.mux.SetProperty("streamable", true)

	case params.OutputTypeHLS:
		b.mux, err = b.buildHLSMux(p)

	default:
		err = errors.ErrInvalidInput("output type")
	}
	if err != nil {
		return err
	}

	return b.bin.Add(b.mux)
}

func (b *Bin) buildHLSMux(p *params.Params) (*gst.Element, error) {
	// Create Sink
	sink, err := gst.NewElement("splitmuxsink")
	if err != nil {
		return nil, err
	}

	// TODO make this a request parameter?
	// 6s segments
	if err = sink.SetProperty("max-size-time", uint64(time.Duration(p.SegmentDuration)*time.Second)); err != nil {
		return nil, err
	}

	if err = sink.SetProperty("async-finalize", true); err != nil {
		return nil, err
	}
	if err = sink.SetProperty("muxer-factory", "mpegtsmux"); err != nil {
		return nil, err
	}

	filenamePattern := fmt.Sprintf("%s_%%05d.ts", p.LocalFilePrefix)
	if err = sink.SetProperty("location", filenamePattern); err != nil {
		return nil, err
	}

	return sink, err
}
