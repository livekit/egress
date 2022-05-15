package input

import (
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/livekit-egress/pkg/pipeline/source"
)

// TODO: save mp4 files as TS then remux to avoid losing everything on failure
func Build(conf *config.Config, p *params.Params) (*Bin, error) {
	b := &Bin{
		bin:      gst.NewBin("input"),
		isStream: p.IsStream,
	}

	// source
	err := b.buildSource(conf, p)
	if err != nil {
		return nil, err
	}

	if p.SkipPipeline {
		return b, nil
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
	if p.OutputType == params.OutputTypeRaw {
		if p.VideoEnabled {
			return nil, errors.ErrNotSupported("raw video output")
		}
		ghostPad = gst.NewGhostPad("src", b.audioElements[len(b.audioElements)-1].GetStaticPad("src"))
	} else {
		ghostPad = gst.NewGhostPad("src", b.mux.GetStaticPad("src"))
	}
	if !b.bin.AddPad(ghostPad.Pad) {
		return nil, errors.ErrGhostPadFailed
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
		// When output is raw, don't build mux
		return nil
	case params.OutputTypeOGG:
		b.mux, err = gst.NewElement("oggmux")

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

	default:
		err = errors.ErrInvalidInput("output type")
	}
	if err != nil {
		return err
	}

	return b.bin.Add(b.mux)
}
