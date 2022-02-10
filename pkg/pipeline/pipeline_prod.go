//go:build !test
// +build !test

package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"
)

// gst.Init needs to be called before using gst but after gst package loads
var initialized = false

const pipelineSource = "pipeline"

type Pipeline struct {
	mu sync.Mutex

	pipeline *gst.Pipeline
	loop     *glib.MainLoop

	output  *OutputBin
	removed map[string]bool

	started   chan struct{}
	startedAt time.Time
	closed    chan struct{}

	err error
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
		started:  make(chan struct{}),
		closed:   make(chan struct{}),
	}, nil
}

func (p *Pipeline) Run() error {
	// add watch
	p.loop = glib.NewMainLoop(glib.MainContextDefault(), false)
	p.pipeline.GetPipelineBus().AddWatch(func(msg *gst.Message) bool {
		switch msg.Type() {
		case gst.MessageEOS:
			// EOS received - close and return
			logger.Debugw("EOS received, stopping pipeline")
			_ = p.pipeline.BlockSetState(gst.StateNull)
			logger.Debugw("pipeline stopped")

			p.loop.Quit()
			return false
		case gst.MessageError:
			// handle error if possible, otherwise close and return
			gErr := msg.ParseError()
			err, handled := p.handleError(gErr)
			if handled {
				logger.Errorw("error handled", errors.New(gErr.Error()))
			} else {
				p.err = err
				p.loop.Quit()
				return false
			}
		case gst.MessageStateChanged:
			if msg.Source() == pipelineSource && p.startedAt.IsZero() {
				_, newState := msg.ParseStateChanged()
				if newState == gst.StatePlaying {
					p.startedAt = time.Now()
					close(p.started)
				}
			}
		default:
			logger.Debugw(msg.String())
		}

		return true
	})

	// set state to playing (this does not start the pipeline)
	if err := p.pipeline.SetState(gst.StatePlaying); err != nil {
		return err
	}

	// run main loop
	p.loop.Run()
	return p.err
}

func (p *Pipeline) GetStartTime() time.Time {
	select {
	case <-p.started:
		return p.startedAt
	case <-p.closed:
		return p.startedAt
	}
}

func (p *Pipeline) AddOutput(url string) error {
	return p.output.AddRtmpSink(url)
}

func (p *Pipeline) RemoveOutput(url string) error {
	return p.output.RemoveRtmpSink(url)
}

// Abort can only be called before the pipeline has started
func (p *Pipeline) Abort() {
	select {
	case <-p.closed:
		return
	default:
		close(p.closed)
	}
}

// Close waits for the pipeline to start before closing
func (p *Pipeline) Close() {
	select {
	case <-p.closed:
		return
	case <-p.started:
		close(p.closed)

		logger.Debugw("sending EOS to pipeline")
		p.pipeline.SendEvent(gst.NewEOSEvent())
	}
}

// handleError returns true if the error has been handled, false if the pipeline should quit
func (p *Pipeline) handleError(gErr *gst.GError) (error, bool) {
	err := errors.New(gErr.Error())

	element, reason, ok := parseDebugInfo(gErr.DebugString())
	if !ok {
		logger.Errorw("failed to parse pipeline error", err, "debug", gErr.DebugString())
		return err, false
	}

	switch reason {
	case GErrNoURI, GErrCouldNotConnect:
		// bad URI or could not connect. Remove rtmp output
		if err := p.output.RemoveSinkByName(element); err != nil {
			logger.Errorw("failed to remove sink", err)
			return err, false
		}
		p.removed[element] = true
		return err, true
	case GErrFailedToStart:
		// returned after an added rtmp sink failed to start
		// should be preceded by a GErrNoURI on the same sink
		handled := p.removed[element]
		if !handled {
			logger.Errorw("element failed to start", err)
		}
		return err, handled
	case GErrStreamingStopped:
		// returned by queue after rtmp sink could not connect
		// should be preceded by a GErrCouldNotConnect on associated sink
		handled := false
		if strings.HasPrefix(element, "queue_") {
			handled = p.removed[fmt.Sprint("sink_", element[6:])]
		}
		if !handled {
			logger.Errorw("streaming sink stopped", err)
		}
		return err, handled
	default:
		// input failure or file write failure. Fatal
		logger.Errorw("pipeline error", err, "debug", gErr.DebugString())
		return err, false
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

func requireLink(src, sink *gst.Pad) error {
	if linkReturn := src.Link(sink); linkReturn != gst.PadLinkOK {
		return fmt.Errorf("pad link: %s", linkReturn.String())
	}
	return nil
}
