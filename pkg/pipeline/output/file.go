package output

import (
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/types"
)

type FileOutput struct {
	mux  *gst.Element
	sink *gst.Element
}

func buildFileOutput(bin *gst.Bin, p *config.OutputConfig) (*FileOutput, error) {
	mux, err := buildFileMux(p)
	if err != nil {
		return nil, err
	}

	// create elements
	sink, err := gst.NewElement("filesink")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("location", p.LocalFilepath); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("sync", false); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	if err = bin.AddMany(mux, sink); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return &FileOutput{
		mux:  mux,
		sink: sink,
	}, nil
}

func buildFileMux(p *config.OutputConfig) (*gst.Element, error) {
	switch p.OutputType {
	case types.OutputTypeOGG:
		return gst.NewElement("oggmux")

	case types.OutputTypeIVF:
		return gst.NewElement("avmux_ivf")

	case types.OutputTypeMP4:
		return gst.NewElement("mp4mux")

	case types.OutputTypeWebM:
		return gst.NewElement("webmmux")

	default:
		return nil, errors.ErrInvalidInput("output type")
	}
}

func (o *FileOutput) Link(audioTee, videoTee *gst.Element) error {
	if audioTee != nil {
		teePad := audioTee.GetRequestPad("src_%u")
		muxPad := o.mux.GetRequestPad("audio_%u")
		if err := builder.LinkPads("audio tee", teePad, "file mux", muxPad); err != nil {
			return err
		}
	}

	if videoTee != nil {
		teePad := videoTee.GetRequestPad("src_%u")
		muxPad := o.mux.GetRequestPad("video_%u")
		if err := builder.LinkPads("video tee", teePad, "file mux", muxPad); err != nil {
			return err
		}
	}

	if err := o.mux.Link(o.sink); err != nil {
		return errors.ErrPadLinkFailed("mux", "sink", err.Error())
	}

	return nil
}
