package output

import (
	"fmt"
	"time"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/protocol/livekit"
)

type SegmentOutput struct {
	*outputBase

	sink      *gst.Element
	h264parse *gst.Element

	startDate time.Time
}

type FirstSampleMetadata struct {
	StartDate int64 // Real time date of the first media sample
}

func (b *Bin) buildSegmentOutput(p *config.PipelineConfig, out *config.OutputConfig) (*SegmentOutput, error) {
	s := &SegmentOutput{}

	base, err := b.buildOutputBase(p)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	h264parse, err := gst.NewElement("h264parse")
	if err != nil {
		return nil, err
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

	_, err = sink.Connect("format-location-full", func(self *gst.Element, fragmentId uint, firstSample *gst.Sample) string {
		if s.startDate.IsZero() {
			now := time.Now()

			s.startDate = now.Add(-firstSample.GetBuffer().PresentationTimestamp())

			mdata := FirstSampleMetadata{
				StartDate: now.UnixNano(),
			}
			str := gst.MarshalStructure(mdata)
			msg := gst.NewElementMessage(sink, str)
			sink.GetBus().Post(msg)
		}

		switch out.SegmentParams.SegmentSuffix {
		case livekit.SegmentedFileSuffix_TIMESTAMP:
			ts := s.startDate.Add(firstSample.GetBuffer().PresentationTimestamp())

			return fmt.Sprintf("%s_%s%03d.ts", out.LocalFilePrefix, ts.Format("20060102150405"), ts.UnixMilli()%1000)
		default:
			return fmt.Sprintf("%s_%05d.ts", out.LocalFilePrefix, fragmentId)
		}
	})
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	if err = b.bin.AddMany(h264parse, sink); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	s.outputBase = base
	s.h264parse = h264parse
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
		if err := o.videoQueue.Link(o.h264parse); err != nil {
			return errors.ErrPadLinkFailed("video queue", "h264parse", err.Error())
		}
		if err := builder.LinkPads(
			"h264parse", o.h264parse.GetStaticPad("src"),
			"split mux", o.sink.GetRequestPad("video"),
		); err != nil {
			return err
		}
	}

	return nil
}
