package output

import (
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/types"
)

type FileOutput struct {
	audioQueue *gst.Element
	videoQueue *gst.Element
	mux        *gst.Element
	sink       *gst.Element
}

func (b *Bin) buildFileOutput(p *config.PipelineConfig, out *config.OutputConfig) (*FileOutput, error) {
	audioQueue, videoQueue, err := b.buildQueues(p)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	mux, err := buildFileMux(out)
	if err != nil {
		return nil, err
	}

	// create elements
	sink, err := gst.NewElement("filesink")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("location", out.LocalFilepath); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("sync", false); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	if err = b.bin.AddMany(mux, sink); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return &FileOutput{
		audioQueue: audioQueue,
		videoQueue: videoQueue,
		mux:        mux,
		sink:       sink,
	}, nil
}

func buildFileMux(out *config.OutputConfig) (*gst.Element, error) {
	switch out.OutputType {
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
	// link audio to mux
	if audioTee != nil {
		if err := builder.LinkPads(
			"audio tee", audioTee.GetRequestPad("src_%u"),
			"audio queue", o.audioQueue.GetStaticPad("sink"),
		); err != nil {
			return err
		}

		if err := builder.LinkPads(
			"audio queue", o.audioQueue.GetStaticPad("src"),
			"file mux", o.mux.GetRequestPad("audio_%u"),
		); err != nil {
			return err
		}
	}

	// link video to mux
	if videoTee != nil {
		if err := builder.LinkPads(
			"video tee", videoTee.GetRequestPad("src_%u"),
			"video queue", o.videoQueue.GetStaticPad("sink"),
		); err != nil {
			return err
		}

		if err := builder.LinkPads(
			"video queue", o.videoQueue.GetStaticPad("src"),
			"file mux", o.mux.GetRequestPad("video_%u"),
		); err != nil {
			return err
		}
	}

	// link mux to sink
	if err := o.mux.Link(o.sink); err != nil {
		return errors.ErrPadLinkFailed("mux", "sink", err.Error())
	}

	return nil
}
