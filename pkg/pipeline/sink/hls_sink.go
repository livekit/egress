package sink

import (
	"fmt"
	"io"
	"os"

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

	prefix string

	nextIndex   int
	currentFile *os.File

	sampleAddedToSegment bool
}

// TODO handle EOS

func NewHlsSink(p *params.Params) (*HlsSink, error) {

	s := &HlsSink{
		logger: p.Logger,
		prefix: p.FilePrefix,
	}

	sink, err := gst.NewElementWithName("appsink", HlsAppSink)
	if err != nil {
		s.logger.Errorw("could not create appsink", err)
		return nil, err
	}

	appSink := app.SinkFromElement(sink)
	//	appSink.SetBufferListSupport(true)
	appSink.SetCallbacks(&app.SinkCallbacks{
		EOSFunc:       func(appSink *app.Sink) {},
		NewSampleFunc: func(appSink *app.Sink) gst.FlowReturn { return s.processSample() },
	})

	s.appSink = appSink

	err = s.CreateSegment()
	if err != nil {
		return nil, err
	}

	return s, nil
}

func (s *HlsSink) GetAppSink() *app.Sink {
	return s.appSink
}

func (s *HlsSink) CreateSegment() error {
	filename := fmt.Sprintf("%s-%d.mp4", s.prefix, s.nextIndex)
	file, err := os.Create(filename)
	if err != nil {
		return err

	}

	s.currentFile = file
	s.nextIndex++

	return nil
}

func (s *HlsSink) processSample() gst.FlowReturn {
	sample := s.appSink.PullSample()
	if sample == nil {
		s.logger.Debugw("PullSample returned no sample")
		return gst.FlowOK
	}

	/*	bufferList := sample.GetBufferList()
		if bufferList == nil {
			s.logger.Debugw("No buffer list in sample")
			return gst.FlowOK
		}

		s.logger.Errorw("buffer list size", nil, "size", bufferList.Length())

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
		})*/

	buf := sample.GetBuffer()
	if buf == nil {
		s.logger.Debugw("No buffer")
		return gst.FlowOK
	}

	err := s.processBuffer(buf)
	if err != nil {
		logger.Errorw("Buffer processing failed", err)
		return gst.FlowError
	}
	return gst.FlowOK

	return gst.FlowOK
}

func (s *HlsSink) processBuffer(buffer *gst.Buffer) error {
	flags := buffer.GetFlags()

	//logger.Errorw("buffer", nil, "flags", flags)

	if /*((flags & gst.BufferFlagDiscont) != 0) ||*/ flags&gst.BufferFlagHeader != 0 {
		size := buffer.GetSize()

		msg := fmt.Sprintf("Init buffer of size %d", size)
		s.logger.Errorw(msg, nil)

		return nil
	}

	duration := buffer.Duration()
	if duration == gst.ClockTimeNone {
		if s.sampleAddedToSegment {
			s.startNewSegment()
		}
	} else {
		s.sampleAddedToSegment = true
	}

	r := buffer.Reader()
	n, err := io.Copy(s.currentFile, r)
	if err != nil {
		return err
	}

	s.logger.Debugw("Copied buffer", "size", n)

	//	msg := fmt.Sprintf("buffer of duration %d", duration/time.Millisecond)
	//	logger.Errorw(msg, nil)

	return nil
}

func (s *HlsSink) startNewSegment() {
	s.currentFile.Close()
	s.CreateSegment()
	s.sampleAddedToSegment = false
}
