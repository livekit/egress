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

	s := &HlsSink{
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

	bufferList := sample.GetBufferList()
	if bufferList == nil {
		s.logger.Debugw("No buffer list in sample")
		return gst.FlowOK
	}

	bufferList.ForEach(func(buf *gst.Buffer, idx uint) bool {
		if buf == nil {
			s.logger.Debugw("No buffer")
			return true
		}

		err := s.ProcessBuffer(buf)
		if err != nil {
			logger.Errorw("Buffer processing failed", err)
			return false
		}
		return true
	})
	return gst.FlowOK
}

func (s *HlsSink) ProcessBuffer(buffer *gst.Buffer) error {
	flags := buffer.GetFlags()

	if ((flags & gst.BufferFlagDiscont) != 0) || (flags&gst.BufferFlagHeader != 0) {
		size := buffer.GetSize()

		msg := fmt.Sprintf("Init buffer of size %d", size)
		s.logger.Errorw(msg, nil)

		return nil
	}

	duration := buffer.Duration()
	if duration == gst.ClockTimeNone {
		s.logger.Debugw("Invalid duration")
		return nil
	}

	msg := fmt.Sprintf("buffer of duration %d", duration/time.Millisecond)
	logger.Errorw(msg, nil)

	return nil
}
