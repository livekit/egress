package composite

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
)

// gst.Init needs to be called before using gst but after gst package loads
var initialized = false

const (
	pipelineSource = "pipeline"
)

type compositePipeline struct {
	// pipeline
	pipeline *gst.Pipeline
	in       *inputBin
	out      *outputBin

	// info
	mu       sync.Mutex
	isStream bool
	info     *livekit.EgressInfo
	removed  map[string]bool
	closed   chan struct{}
}

func NewPipeline(conf *config.Config, params *config.Params) (*compositePipeline, error) {
	if !initialized {
		gst.Init(nil)
		initialized = true
	}

	// create input bin
	in, err := newInputBin(conf, params)
	if err != nil {
		return nil, err
	}

	// create output bin
	out, err := newOutputBin(params)
	if err != nil {
		return nil, err
	}

	// link elements
	pipeline, err := buildPipeline(in, out, params.IsStream)
	if err != nil {
		return nil, err
	}

	return &compositePipeline{
		pipeline: pipeline,
		in:       in,
		out:      out,
		isStream: params.IsStream,
		info:     params.Info,
		removed:  make(map[string]bool),
		closed:   make(chan struct{}),
	}, nil
}

func (p *compositePipeline) Info() *livekit.EgressInfo {
	return p.info
}

func (p *compositePipeline) Run() *livekit.EgressInfo {
	// wait for room to start
	logger.Debugw("Waiting for room to start")
	select {
	case <-p.in.StartRecording():
		logger.Debugw("Room started")
	case <-p.closed:
		logger.Debugw("Egress aborted")
	}

	// close when room ends
	go func() {
		<-p.in.EndRecording()
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
				p.info.Error = err.Error()
				loop.Quit()
				return false
			}
		case gst.MessageStateChanged:
			if msg.Source() == pipelineSource && p.info.StartedAt == 0 {
				_, newState := msg.ParseStateChanged()
				if newState == gst.StatePlaying {
					p.info.StartedAt = time.Now().UnixNano()
				}
			}
		default:
			logger.Debugw(msg.String())
		}

		return true
	})

	// set state to playing (this does not start the pipeline)
	if err := p.pipeline.SetState(gst.StatePlaying); err != nil {
		p.info.Error = err.Error()
		return p.info
	}

	// run main loop
	loop.Run()
	p.info.EndedAt = time.Now().UnixNano()

	// close input source
	p.in.Close()
	return p.info
}

func (p *compositePipeline) UpdateStream(req *livekit.UpdateStreamRequest) error {
	if !p.isStream {
		return errors.ErrInvalidRPC
	}

	for _, url := range req.AddOutputUrls {
		if err := p.out.addSink(url); err != nil {
			return err
		}
	}
	for _, url := range req.RemoveOutputUrls {
		if err := p.out.removeSink(url); err != nil {
			return err
		}
	}

	return nil
}

func (p *compositePipeline) Stop() {
	select {
	case <-p.closed:
		return
	default:
		close(p.closed)

		logger.Debugw("sending EOS to pipeline")
		p.pipeline.SendEvent(gst.NewEOSEvent())
	}
}

func buildPipeline(in *inputBin, out *outputBin, isStream bool) (*gst.Pipeline, error) {
	// create pipeline
	pipeline, err := gst.NewPipeline("pipeline")
	if err != nil {
		return nil, err
	}

	// add bins to pipeline
	if err = pipeline.AddMany(in.bin.Element, out.bin.Element); err != nil {
		return nil, err
	}

	// link audio elements
	if in.audioQueue != nil {
		if err := gst.ElementLinkMany(in.audioElements...); err != nil {
			return nil, err
		}

		var muxAudioPad *gst.Pad
		if isStream {
			muxAudioPad = in.mux.GetRequestPad("audio")
		} else {
			muxAudioPad = in.mux.GetRequestPad("audio_%u")
		}

		if linkReturn := in.audioQueue.GetStaticPad("src").Link(muxAudioPad); linkReturn != gst.PadLinkOK {
			return nil, fmt.Errorf("audio mux pad link failed: %s", linkReturn.String())
		}
	}

	// link video elements
	if in.videoQueue != nil {
		if err := gst.ElementLinkMany(in.videoElements...); err != nil {
			return nil, err
		}

		var muxVideoPad *gst.Pad
		if isStream {
			muxVideoPad = in.mux.GetRequestPad("video")
		} else {
			muxVideoPad = in.mux.GetRequestPad("video_%u")
		}

		if linkReturn := in.videoQueue.GetStaticPad("src").Link(muxVideoPad); linkReturn != gst.PadLinkOK {
			return nil, fmt.Errorf("video mux pad link failed: %s", linkReturn.String())
		}
	}

	// stream tee and sinks
	for _, sink := range out.sinks {
		// link queue to rtmp sink
		if err := sink.queue.Link(sink.sink); err != nil {
			return nil, err
		}

		pad := out.tee.GetRequestPad("src_%u")
		sink.pad = pad.GetName()

		// link tee to queue
		if linkReturn := pad.Link(sink.queue.GetStaticPad("sink")); linkReturn != gst.PadLinkOK {
			return nil, fmt.Errorf("tee pad link failed: %s", linkReturn.String())
		}
	}

	// link bins
	if err := in.bin.Link(out.bin.Element); err != nil {
		return nil, err
	}

	return pipeline, nil
}

// handleError returns true if the error has been handled, false if the pipeline should quit
func (p *compositePipeline) handleError(gErr *gst.GError) (error, bool) {
	err := errors.New(gErr.Error())

	element, reason, ok := parseDebugInfo(gErr.DebugString())
	if !ok {
		logger.Errorw("failed to parse pipeline error", err, "debug", gErr.DebugString())
		return err, false
	}

	switch reason {
	case errors.GErrNoURI, errors.GErrCouldNotConnect:
		// bad URI or could not connect. Remove rtmp output
		if err := p.out.removeSinkByName(element); err != nil {
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
