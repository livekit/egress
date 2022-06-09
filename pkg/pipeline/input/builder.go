package input

import (
	"fmt"
	"time"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/egress/pkg/pipeline/source"
)

// TODO: save mp4 files as TS then remux to avoid losing everything on failure
func Build(conf *config.Config, p *params.Params) (*Bin, error) {
	b := &Bin{
		bin: gst.NewBin("input"),
	}

	// source
	err := b.buildSource(conf, p)
	if err != nil {
		return nil, err
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

	// mux
	err = b.buildMux(p)
	if err != nil {
		return nil, err
	}

	// create ghost pad
	var ghostPad *gst.GhostPad
	if b.mux != nil {
		// For HLS, there will be no 'src' pad
		pad := b.mux.GetStaticPad("src")
		if pad != nil {
			ghostPad = gst.NewGhostPad("src", pad)
		}
	} else if b.audioQueue != nil {
		ghostPad = gst.NewGhostPad("src", b.audioQueue.GetStaticPad("src"))
	} else if b.videoQueue != nil {
		ghostPad = gst.NewGhostPad("src", b.videoQueue.GetStaticPad("src"))
	}

	if ghostPad != nil {
		if !b.bin.AddPad(ghostPad.Pad) {
			return nil, errors.ErrGhostPadFailed
		}
	}

	return b, nil
}

func (b *Bin) buildSource(conf *config.Config, p *params.Params) error {
	var err error
	if p.IsWebSource {
		b.Source, err = source.NewWebSource(conf, p)
	} else {
		b.Source, err = source.NewSDKSource(p)
	}
	return err
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
		err = b.mux.Set("streamable", true)
	case params.OutputTypeHLS:
		b.mux, err = b.buildHlsMux(p)
		if err != nil {
			return err
		}
	default:
		err = errors.ErrInvalidInput("output type")
	}
	if err != nil {
		return err
	}

	return b.bin.Add(b.mux)
}

func (b *Bin) buildHlsMux(p *params.Params) (*gst.Element, error) {
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
