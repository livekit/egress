package output

import (
	"fmt"
	"sync"
	"time"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
)

type SegmentOutput struct {
	*outputBase

	sink *gst.Element

	startedAtLock sync.Mutex
	startedAt     time.Time
	startedAtTs   time.Duration // timestamp of the 1st sample
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
		s.startedAtLock.Lock()
		defer s.startedAtLock.Unlock()

		if s.startedAtTs == 0 {
			s.startedAtTs = firstSample.GetBuffer().PresentationTimestamp()
		}

		fragmentStart := s.startedAt.Add(-s.startedAtTs).Add(firstSample.GetBuffer().PresentationTimestamp())
		fmt.Println("FORMAT-LOCATION", fragmentId, firstSample.GetBuffer().PresentationTimestamp(), s.startedAt, fragmentStart, len(firstSample.GetBuffer().Bytes()), firstSample.GetCaps())

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

func (o *SegmentOutput) UpdateStartTime(t time.Time) {
	o.startedAtLock.Lock()
	defer o.startedAtLock.Unlock()

	fmt.Println("UPDATESTARTTIME")

	o.startedAt = t
}
