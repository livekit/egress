//go:build !test
// +build !test

package pipeline

import (
	"errors"
	"fmt"
	"strings"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"
)

// gst.Init needs to be called before using gst but after gst package loads
var initialized = false

type Pipeline struct {
	pipeline *gst.Pipeline
	output   *OutputBin
	removed  map[string]bool

	started chan struct{}
	closed  chan struct{}
}

func NewRtmpPipeline(urls []string, options *livekit.RecordingOptions) (*Pipeline, error) {
	if !initialized {
		gst.Init(nil)
		initialized = true
	}

	input, err := newInputBin(true, options)
	if err != nil {
		return nil, err
	}
	output, err := newRtmpOutputBin(urls)
	if err != nil {
		return nil, err
	}

	return newPipeline(input, output)
}

func NewFilePipeline(filename string, options *livekit.RecordingOptions) (*Pipeline, error) {
	if !initialized {
		gst.Init(nil)
		initialized = true
	}

	input, err := newInputBin(false, options)
	if err != nil {
		return nil, err
	}
	output, err := newFileOutputBin(filename)
	if err != nil {
		return nil, err
	}

	return newPipeline(input, output)
}

func newPipeline(input *InputBin, output *OutputBin) (*Pipeline, error) {
	// elements must be added to pipeline before linking
	pipeline, err := gst.NewPipeline("pipeline")
	if err != nil {
		return nil, err
	}

	// add bins to pipeline
	if err = pipeline.AddMany(input.bin.Element, output.bin.Element); err != nil {
		return nil, err
	}

	// link bin elements
	if err = input.Link(); err != nil {
		return nil, err
	}
	if err = output.Link(); err != nil {
		return nil, err
	}

	// link bins
	if err = input.bin.Link(output.bin.Element); err != nil {
		return nil, err
	}

	return &Pipeline{
		pipeline: pipeline,
		output:   output,
		removed:  make(map[string]bool),
		started:  make(chan struct{}, 1),
		closed:   make(chan struct{}),
	}, nil
}

func (p *Pipeline) Start() error {
	loop := glib.NewMainLoop(glib.MainContextDefault(), false)
	p.pipeline.GetPipelineBus().AddWatch(func(msg *gst.Message) bool {
		switch msg.Type() {
		case gst.MessageEOS:
			logger.Debugw("EOS received")
			_ = p.pipeline.BlockSetState(gst.StateNull)
			logger.Debugw("pipeline stopped")
			loop.Quit()
			return false
		case gst.MessageError:
			gErr := msg.ParseError()
			handled := p.handleError(gErr)
			if !handled {
				loop.Quit()
				return false
			}
			logger.Debugw("handled error", "error", gErr.Error())
		default:
			logger.Debugw(msg.String())
		}
		return true
	})

	// start playing
	err := p.pipeline.SetState(gst.StatePlaying)
	if err != nil {
		return err
	}

	// Block and iterate on the main loop
	close(p.started)
	loop.Run()
	return nil
}

// handleError returns true if the error has been handled, false if the pipeline should quit
func (p *Pipeline) handleError(gErr *gst.GError) bool {
	element, reason, ok := parseDebugInfo(gErr.DebugString())
	if !ok {
		logger.Errorw("failed to parse pipeline error", errors.New(gErr.Error()),
			"debug", gErr.DebugString(),
		)
		return false
	}

	switch reason {
	case GErrNoURI, GErrCouldNotConnect:
		// bad URI or could not connect. Remove rtmp output
		if err := p.output.RemoveSinkByName(element); err != nil {
			logger.Errorw("failed to remove sink", err)
			return false
		}
		p.removed[element] = true
		return true
	case GErrFailedToStart:
		// returned after an added rtmp sink failed to start
		// should be preceded by GErrNoURI on the same sink
		return p.removed[element]
	case GErrStreamingStopped:
		// returned by queue after rtmp sink could not connect
		// should be preceded by GErrCouldNotConnect on associated sink
		if strings.HasPrefix(element, "queue_") {
			return p.removed[fmt.Sprint("sink_", element[6:])]
		}
		return false
	default:
		// input failure or file write failure. Fatal
		logger.Errorw("pipeline error", errors.New(gErr.Error()),
			"debug", gErr.DebugString(),
		)
		return false
	}
}

// Debug info comes in the following format:
// file.c(line): method_name (): /GstPipeline:pipeline/GstBin:bin_name/GstElement:element_name:\nError message
func parseDebugInfo(debug string) (element string, reason string, ok bool) {
	end := strings.Index(debug, ":\n")
	if end == -1 {
		return
	}
	start := strings.LastIndex(debug[:end], ":")
	if start == -1 {
		return
	}
	element = debug[start+1 : end]
	reason = debug[end+2:]
	if strings.HasPrefix(reason, GErrCouldNotConnect) {
		reason = GErrCouldNotConnect
	}
	ok = true
	return
}

func (p *Pipeline) AddOutput(url string) error {
	return p.output.AddRtmpSink(url)
}

func (p *Pipeline) RemoveOutput(url string) error {
	return p.output.RemoveRtmpSink(url)
}

func (p *Pipeline) Close() {
	<-p.started
	select {
	case <-p.closed:
		return
	default:
		close(p.closed)
		logger.Debugw("sending EOS to pipeline")
		p.pipeline.SendEvent(gst.NewEOSEvent())
	}
}

func requireLink(src, sink *gst.Pad) error {
	if linkReturn := src.Link(sink); linkReturn != gst.PadLinkOK {
		return fmt.Errorf("pad link: %s", linkReturn.String())
	}
	return nil
}
