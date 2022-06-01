package sink

import (
	"fmt"
	"time"

	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/logger"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
)

const (
	HlsAppSink = "hlsAppSink"
)

type HlsSink struct {
	logger  logger.Logger
	appSink *app.Sink
}

func NewHlsSink(p *params.Params) (*HlsSink, error) {

	s := HlsSink{
		logger: p.Logger,
	}

	sink, err := gst.NewElementWithName("appsink", HlsAppSink)
	if err != nil {
		s.logger.Errorw("could not create appsink", err)
		return nil, err
	}

	appSink := app.SinkFromElement(sink)
	appSink.SetCallbacks(&app.SinkCallbacks{
		EOSFunc:       func(appSink *app.Sink) {},
		NewSampleFunc: func(appSink *app.Sink) gst.FlowReturn { return s.ProcessSample() },
	})

	s.appSink = appSink

	return s, nil
}

func (s *HlsSink) GetAppSink() *app.Sink {
	return s.appSink
}

func (s *HlsSink) ProcessSample() gst.FlowReturn {
	sample := s.appSink.PullSample()
	if sample == nil {
		s.logger.Debugw("PullSample returned no sample")
		return gst.FlowOK
	}

	segment := sample.GetSegment()
	if segment == nil {
		s.logger.Errorw("No segment in sample", gst.FlowError)
		return gst.FlowError
	}

	bufferList := sample.GetBufferList()
	if bufferList == nil {
		s.logger.Debugw("No buffer list in sample")
		return gst.FlowOK
	}

	ret := bufferList.ForEach(func(buf *gst.Buffer, idx uint) {
		if buf == nil {
			s.logger.Debugw("No buffer")
			return true
		}

		gstErr := s.ProcessBuffer(buf, segment)
		if gstErr != gst.FlowOK {
			logger.Errorw("Buffer processing failed", err)
			return false
		}
		return true
	})
	if ret {
		return gst.FlowOK
	} else {
		return gst.FlowError
	}
}

func (s *HlsSink) ProcesBuffer(buffer *gst.Buffer, segment *gst.Segment) gst.FlowReturn {
	flags := buffer.GetFlags()

	if (flags & gst.BufferFlagDiscont) || flags&gst.BufferFlagHeader {
		size := buffer.GetSize()

		msg := fmt.Sprintf("Init buffer of size %d", size)
		s.logger.Errorw(msg, nil)

		return gst.FlowOK
	}

	duration := buffer.Duration()
	if duration == ClockTimeNone {
		s.logger.Debugw("Invalid duration")
		return gst.FlowOK
	}

	msg := fmt.Sprintf("buffer of duration %d", duration/time.Millisecond)
	log.Errorw(msg, nil)

	return gst.FlowOk
}
