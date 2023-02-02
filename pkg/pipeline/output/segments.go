package output

import (
	"fmt"
	"time"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
)

type SegmentOutput struct {
	sink *gst.Element
}

func buildSegmentOutput(bin *gst.Bin, p *config.OutputConfig) (*SegmentOutput, error) {
	sink, err := gst.NewElement("splitmuxsink")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("max-size-time", uint64(time.Duration(p.SegmentDuration)*time.Second)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("send-keyframe-requests", true); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("async-finalize", true); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("muxer-factory", "mpegtsmux"); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("location", fmt.Sprintf("%s_%%05d.ts", p.LocalFilePrefix)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	if err = bin.Add(sink); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return &SegmentOutput{
		sink: sink,
	}, nil
}

func (o *SegmentOutput) Link(audioTee, videoTee *gst.Element) error {
	// link audio to sink
	if audioTee != nil {
		teePad := audioTee.GetRequestPad("src_%u")
		sinkPad := o.sink.GetRequestPad("audio_%u")
		if err := builder.LinkPads("audio tee", teePad, "file mux", sinkPad); err != nil {
			return err
		}
	}

	// link video to sink
	if videoTee != nil {
		teePad := videoTee.GetRequestPad("src_%u")
		sinkPad := o.sink.GetRequestPad("video")
		if err := builder.LinkPads("video tee", teePad, "file mux", sinkPad); err != nil {
			return err
		}
	}

	return nil
}
