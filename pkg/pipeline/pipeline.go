package pipeline

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/input"
	"github.com/livekit/livekit-egress/pkg/output"
)

// gst.Init needs to be called before using gst but after gst package loads
var initialized = false

const (
	pipelineSource = "pipeline"
)

type Pipeline struct {
	Info *livekit.EgressInfo

	pipeline *gst.Pipeline
	input    input.Bin
	output   output.Bin
	closed   chan struct{}

	mu      sync.Mutex
	removed map[string]bool
}

func FromRequest(conf *config.Config, request *livekit.StartEgressRequest) (*Pipeline, error) {
	// get params
	params, err := config.GetPipelineParams(request)
	if err != nil {
		return nil, err
	}

	return FromParams(conf, params)
}

func FromParams(conf *config.Config, params *config.Params) (*Pipeline, error) {
	if !initialized {
		gst.Init(nil)
		initialized = true
	}

	// create bins
	in, err := input.New(conf, params)
	if err != nil {
		return nil, err
	}
	out, err := output.New(params)
	if err != nil {
		return nil, err
	}

	// create pipeline
	pipeline, err := gst.NewPipeline("pipeline")
	if err != nil {
		return nil, err
	}

	// add bins to pipeline
	if err = pipeline.AddMany(in.Bin().Element, out.Bin().Element); err != nil {
		return nil, err
	}

	// link bin elements
	if err = in.LinkElements(); err != nil {
		return nil, err
	}
	if err = out.LinkElements(); err != nil {
		return nil, err
	}

	// link bins
	if err = in.Bin().Link(out.Bin().Element); err != nil {
		return nil, err
	}

	return &Pipeline{
		Info:     params.Info,
		pipeline: pipeline,
		input:    in,
		output:   out,
		removed:  make(map[string]bool),
		closed:   make(chan struct{}),
	}, nil
}

func (p *Pipeline) Run() *livekit.EgressInfo {
	// wait for room to start
	logger.Debugw("Waiting for room to start")
	select {
	case <-p.input.RoomStarted():
		logger.Debugw("Room started")
	case <-p.closed:
		logger.Debugw("Egress aborted")
	}

	// close when room ends
	go func() {
		<-p.input.RoomEnded()
		p.Stop()
	}()

	// add watch
	loop := glib.NewMainLoop(glib.MainContextDefault(), false)
	p.pipeline.GetPipelineBus().AddWatch(func(msg *gst.Message) bool {
		switch msg.Type() {
		case gst.MessageEOS:
			// EOS received - close and return
			logger.Debugw("EOS received, stopping pipeline")
			_ = p.pipeline.BlockSetState(gst.StateNull)
			logger.Debugw("pipeline stopped")

			loop.Quit()
			return false
		case gst.MessageError:
			// handle error if possible, otherwise close and return
			gErr := msg.ParseError()
			err, handled := p.handleError(gErr)
			if handled {
				logger.Errorw("error handled", err)
			} else {
				p.Info.Error = err.Error()
				loop.Quit()
				return false
			}
		case gst.MessageStateChanged:
			if msg.Source() == pipelineSource && p.Info.StartedAt == 0 {
				_, newState := msg.ParseStateChanged()
				if newState == gst.StatePlaying {
					p.Info.StartedAt = time.Now().UnixNano()
				}
			}
		default:
			logger.Debugw(msg.String())
		}

		return true
	})

	// set state to playing (this does not start the pipeline)
	if err := p.pipeline.SetState(gst.StatePlaying); err != nil {
		p.Info.Error = err.Error()
		return p.Info
	}

	// run main loop
	loop.Run()
	p.Info.EndedAt = time.Now().UnixNano()

	// close input source
	p.input.Close()
	return p.Info
}

func (p *Pipeline) UpdateStream(req *livekit.UpdateStreamRequest) error {
	for _, url := range req.AddOutputUrls {
		if err := p.output.AddSink(url); err != nil {
			return err
		}
	}
	for _, url := range req.RemoveOutputUrls {
		if err := p.output.RemoveSink(url); err != nil {
			return err
		}
	}

	return nil
}

func (p *Pipeline) Stop() {
	select {
	case <-p.closed:
		return
	default:
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
	case errors.GErrNoURI, errors.GErrCouldNotConnect:
		// bad URI or could not connect. Remove rtmp output
		if err := p.output.RemoveSinkByName(element); err != nil {
			logger.Errorw("failed to remove sink", err)
			return err, false
		}
		p.removed[element] = true
		return err, true
	case errors.GErrFailedToStart:
		// returned after an added rtmp sink failed to start
		// should be preceded by a GErrNoURI on the same sink
		handled := p.removed[element]
		if !handled {
			logger.Errorw("element failed to start", err)
		}
		return err, handled
	case errors.GErrStreamingStopped:
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
	if strings.HasPrefix(reason, errors.GErrCouldNotConnect) {
		reason = errors.GErrCouldNotConnect
	}
	ok = true
	return
}
