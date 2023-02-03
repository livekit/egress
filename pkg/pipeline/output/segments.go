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
	audioQueue *gst.Element
	videoQueue *gst.Element
	sink       *gst.Element
}

func buildSegmentOutput(out *config.OutputConfig, bin *gst.Bin, audioQueue, videoQueue *gst.Element) (*SegmentOutput, error) {
	sink, err := gst.NewElement("splitmuxsink")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("max-size-time", uint64(time.Duration(out.SegmentDuration)*time.Second)); err != nil {
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
	if err = sink.SetProperty("location", fmt.Sprintf("%s_%%05d.ts", out.LocalFilePrefix)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	if err = bin.Add(sink); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return &SegmentOutput{
		audioQueue: audioQueue,
		videoQueue: videoQueue,
		sink:       sink,
	}, nil
}

func (o *SegmentOutput) Link(audioTee, videoTee *gst.Element) error {
	// link audio to sink
	if audioTee != nil {
		if err := builder.LinkPads(
			"audio tee", audioTee.GetRequestPad("src_%u"),
			"audio queue", o.audioQueue.GetStaticPad("sink"),
		); err != nil {
			return err
		}

		if err := builder.LinkPads(
			"audio queue", o.audioQueue.GetStaticPad("src"),
			"split mux", o.sink.GetRequestPad("audio_%u"),
		); err != nil {
			return err
		}
	}

	// link video to sink
	if videoTee != nil {
		if err := builder.LinkPads(
			"video tee", videoTee.GetRequestPad("src_%u"),
			"video queue", o.videoQueue.GetStaticPad("sink"),
		); err != nil {
			return err
		}

		if err := builder.LinkPads(
			"video queue", o.videoQueue.GetStaticPad("src"),
			"split mux", o.sink.GetRequestPad("video"),
		); err != nil {
			return err
		}
	}

	return nil
}
