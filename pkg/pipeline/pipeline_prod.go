// +build !test

package pipeline

import (
	"fmt"

	"github.com/livekit/protocol/logger"
	livekit "github.com/livekit/protocol/proto"
	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"
)

// gst.Init needs to be called before using gst but after gst package loads
var initialized = false

type Pipeline struct {
	pipeline *gst.Pipeline
	output   *OutputBin
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

	// TODO: output bin error handling

	return &Pipeline{
		pipeline: pipeline,
		output:   output,
	}, nil
}

func (p *Pipeline) Start() error {
	loop := glib.NewMainLoop(glib.MainContextDefault(), false)
	p.pipeline.GetPipelineBus().AddWatch(func(msg *gst.Message) bool {
		switch msg.Type() {
		case gst.MessageEOS:
			logger.Infow("EOS received")
			_ = p.pipeline.BlockSetState(gst.StateNull)
			logger.Infow("pipeline stopped")
			loop.Quit()
		case gst.MessageError:
			gErr := msg.ParseError()
			logger.Errorw("message error", gErr, "debug", gErr.DebugString())
			loop.Quit()
		default:
			fmt.Println(msg)
		}
		return true
	})

	// start playing
	err := p.pipeline.SetState(gst.StatePlaying)
	if err != nil {
		return err
	}

	// Block and iterate on the main loop
	loop.Run()
	return nil
}

func (p *Pipeline) AddOutput(url string) error {
	return p.output.AddRtmpSink(url)
}

func (p *Pipeline) RemoveOutput(url string) error {
	return p.output.RemoveRtmpSink(url)
}

func (p *Pipeline) Close() {
	logger.Debugw("Sending EOS to pipeline")
	p.pipeline.SendEvent(gst.NewEOSEvent())
}

func requireLink(src, sink *gst.Pad) error {
	if linkReturn := src.Link(sink); linkReturn != gst.PadLinkOK {
		return fmt.Errorf("pad link: %s", linkReturn.String())
	}
	return nil
}
