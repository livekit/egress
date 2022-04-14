package input

import (
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/protocol/livekit"

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
	ghostPad := gst.NewGhostPad("src", b.mux.GetStaticPad("src"))
	if !b.bin.AddPad(ghostPad.Pad) {
		return nil, errors.ErrGhostPadFailed
	}

	return b, nil
}

func (b *Bin) buildSource(conf *config.Config, p *params.Params) error {
	var err error
	if p.IsWebInput {
		b.Source, err = source.NewWebSource(conf, p)
	} else {
		b.Source, err = source.NewSDKSource(p)
	}
	return err
}

func (b *Bin) buildMux(p *params.Params) error {
	var err error
	if p.IsStream {
		switch p.StreamProtocol {
		case livekit.StreamProtocol_RTMP:
			b.mux, err = gst.NewElement("flvmux")
			if err != nil {
				return err
			}
			err = b.mux.Set("streamable", true)
		}
	} else {
		switch p.FileType {
		case livekit.EncodedFileType_MP4:
			b.mux, err = gst.NewElement("mp4mux")
			if err != nil {
				return err
			}
			err = b.mux.SetProperty("faststart", true)

		case livekit.EncodedFileType_OGG:
			b.mux, err = gst.NewElement("oggmux")
		}
	}
	if err != nil {
		return err
	}

	return b.bin.Add(b.mux)
}
