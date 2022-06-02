package sink

import (
	"bytes"
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
	trackCount           int
	initSegmentWritten   bool
	tempBuffer           *bytes.Buffer
}

// TODO handle EOS

func NewHlsSink(p *params.Params) (*HlsSink, error) {

	s := &HlsSink{
		logger:     p.Logger,
		prefix:     p.FilePrefix,
		tempBuffer: &bytes.Buffer{},
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

	if p.VideoTrackID != "" {
		s.trackCount++
	}
	if p.AudioTrackID != "" {
		s.trackCount++
	}

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

	s.logger.Debugw("Started new segment", "filename", filename)

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
}

func (s *HlsSink) processBuffer(buffer *gst.Buffer) error {
	var n int64
	var err error

	s.logger.Debugw("New buffer", "size", buffer.GetSize(), "flags", buffer.GetFlags())

	duration := buffer.Duration()
	if duration == gst.ClockTimeNone {
		if s.sampleAddedToSegment {
			s.startNewSegment()
		}
	} else {
		if !s.initSegmentWritten {
			// The init segment is done. The content of the temp buffer is the MOOF atom for the first data segment, so flush it there.
			s.startNewSegment()
			s.initSegmentWritten = true
			n, err := io.Copy(s.currentFile, s.tempBuffer)
			if err != nil {
				return err
			}
			s.logger.Debugw("Finalized Init buffer")
			s.logger.Debugw("Wrote buffer", "size", n)
		}

		s.sampleAddedToSegment = true
	}

	r := buffer.Reader()
	if !s.initSegmentWritten {
		// Flush the temp buffer to the init segment and store the new buffer
		n, err = io.Copy(s.currentFile, s.tempBuffer)
		if err != nil {
			return err
		}
		_, err = io.Copy(s.tempBuffer, r)
		if err != nil {
			return err
		}
	} else {
		n, err = io.Copy(s.currentFile, r)
		if err != nil {
			return err
		}
	}

	s.logger.Debugw("Wrote buffer", "size", n)

	//	msg := fmt.Sprintf("buffer of duration %d", duration/time.Millisecond)
	//	logger.Errorw(msg, nil)

	return nil
}

func (s *HlsSink) startNewSegment() {
	s.currentFile.Close()
	s.CreateSegment()
	s.sampleAddedToSegment = false
}
