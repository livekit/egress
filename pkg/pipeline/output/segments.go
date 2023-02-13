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
	*outputBase

	sink      *gst.Element
	startDate time.Time
}

type FirstSampleMetadata struct {
	StartDate time.Time // Real time date of the 0 PTS
}

func (b *Bin) buildSegmentOutput(p *config.PipelineConfig, out *config.OutputConfig) (*SegmentOutput, error) {
	s := &SegmentOutput{}

	base, err := b.buildOutputBase(p)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

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

	_, err = sink.Connect("format-location-full", func(self *gst.Element, fragmentId uint, firstSample *gst.Sample) string {
		if s.startDate.IsZero() {
			s.startDate = time.Now().Add(-firstSample.GetBuffer().PresentationTimestamp())

			mdata := FirstSampleMetadata{
				StartDate: s.startDate,
			}
			str := gst.MarshalStructure(mdata)
			msg := gst.NewElementMessage(sink, str)
			sink.GetBus().Post(msg)
		}

		return fmt.Sprintf("seg_%d", fragmentId)
	})
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	if err = b.bin.Add(sink); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	s.outputBase = base
	s.sink = sink

	return s, nil
}

func (o *SegmentOutput) Link() error {
	// link audio to sink
	if o.audioQueue != nil {
		if err := builder.LinkPads(
			"audio queue", o.audioQueue.GetStaticPad("src"),
			"split mux", o.sink.GetRequestPad("audio_%u"),
		); err != nil {
			return err
		}
	}

	// link video to sink
	if o.videoQueue != nil {
		if err := builder.LinkPads(
			"video queue", o.videoQueue.GetStaticPad("src"),
			"split mux", o.sink.GetRequestPad("video"),
		); err != nil {
			return err
		}
	}

	return nil
}
