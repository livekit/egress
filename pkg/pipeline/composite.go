package pipeline

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/input"
	"github.com/livekit/livekit-egress/pkg/pipeline/output"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/livekit-egress/pkg/pipeline/sink"
	"github.com/livekit/livekit-egress/pkg/pipeline/source"
)

// gst.Init needs to be called before using gst but after gst package loads
var initialized = false

const (
	pipelineSource = "pipeline"
)

type compositePipeline struct {
	*params.Params

	// gstreamer
	pipeline *gst.Pipeline
	in       *input.Bin
	out      *output.Bin
	loop     *glib.MainLoop

	// internal
	mu      sync.RWMutex
	started bool
	removed map[string]bool
	closed  chan struct{}
}

func NewCompositePipeline(conf *config.Config, p *params.Params) (*compositePipeline, error) {
	if !initialized {
		gst.Init(nil)
		initialized = true
	}

	// create input bin
	in, err := input.Build(conf, p)
	if err != nil {
		return nil, err
	}

	// create output bin
	out, err := output.Build(p)
	if err != nil {
		return nil, err
	}

	// create pipeline
	pipeline, err := gst.NewPipeline("pipeline")
	if err != nil {
		return nil, err
	}

	// add bins to pipeline
	if err = pipeline.AddMany(in.Element(), out.Element()); err != nil {
		return nil, err
	}

	// link input elements
	if err = in.Link(); err != nil {
		return nil, err
	}

	// link output elements
	if err = out.Link(); err != nil {
		return nil, err
	}

	// link bins
	if err := in.Bin().Link(out.Element()); err != nil {
		return nil, err
	}

	return &compositePipeline{
		Params:   p,
		pipeline: pipeline,
		in:       in,
		out:      out,
		removed:  make(map[string]bool),
		closed:   make(chan struct{}),
	}, nil
}

func (p *compositePipeline) GetInfo() *livekit.EgressInfo {
	return p.Info
}

func (p *compositePipeline) Run() *livekit.EgressInfo {
	// wait until room is ready
	start := p.in.StartRecording()
	if start != nil {
		select {
		case <-p.closed:
			p.Info.Status = livekit.EgressStatus_EGRESS_COMPLETE
			p.in.Close()
			return p.Info
		case <-start:
			// continue
		}
	}

	// close when room ends
	go func() {
		<-p.in.EndRecording()
		p.Stop()
	}()

	// add watch
	p.loop = glib.NewMainLoop(glib.MainContextDefault(), false)
	p.pipeline.GetPipelineBus().AddWatch(p.messageWatch)

	// set state to playing (this does not start the pipeline)
	if err := p.pipeline.SetState(gst.StatePlaying); err != nil {
		p.Info.Error = err.Error()
		return p.Info
	}

	// run main loop
	p.loop.Run()

	// close input source
	p.in.Close()

	// upload file
	var err error
	if !p.IsStream {
		switch u := p.FileUpload.(type) {
		case *livekit.S3Upload:
			p.Logger.Debugw("uploading to s3")
			p.FileInfo.Location, err = sink.UploadS3(u, p.FileParams)
		case *livekit.GCPUpload:
			p.Logger.Debugw("uploading to gcp")
			p.FileInfo.Location, err = sink.UploadGCP(u, p.FileParams)
		case *livekit.AzureBlobUpload:
			p.Logger.Debugw("uploading to azure")
			p.FileInfo.Location, err = sink.UploadAzure(u, p.FileParams)
		default:
			p.FileInfo.Location = p.Filepath
		}
	}
	if err != nil {
		p.Logger.Errorw("could not upload file", err)
		p.Info.Error = err.Error()
	}

	// return result
	p.Info.Status = livekit.EgressStatus_EGRESS_COMPLETE
	return p.Info
}

func (p *compositePipeline) messageWatch(msg *gst.Message) bool {
	switch msg.Type() {
	case gst.MessageEOS:
		// EOS received - close and return
		p.Logger.Debugw("EOS received, stopping pipeline")
		_ = p.pipeline.BlockSetState(gst.StateNull)
		p.Logger.Debugw("pipeline stopped")

		p.loop.Quit()

		var endedAt int64
		switch s := p.in.Source.(type) {
		case *source.SDKSource:
			endedAt = s.GetEndTime()
		case *source.WebSource:
			endedAt = time.Now().UnixNano()
		}

		// add end times to egress info
		if p.IsStream {
			p.mu.RLock()
			for _, info := range p.StreamInfo {
				info.EndedAt = endedAt
			}
			p.mu.RUnlock()
		} else {
			p.FileInfo.EndedAt = endedAt
		}

		return false

	case gst.MessageError:
		// handle error if possible, otherwise close and return
		err, handled := p.handleError(msg.ParseError())
		if !handled {
			p.Info.Error = err.Error()
			p.loop.Quit()
			return false
		}

	case gst.MessageStateChanged:
		if p.started {
			return true
		}

		_, newState := msg.ParseStateChanged()
		if newState != gst.StatePlaying {
			return true
		}

		switch msg.Source() {
		case source.AudioAppSource, source.VideoAppSource:
			p.in.Playing(msg.Source())

		case pipelineSource:
			p.started = true

			var startedAt int64
			switch s := p.in.Source.(type) {
			case *source.SDKSource:
				startedAt = s.GetStartTime()
			case *source.WebSource:
				startedAt = time.Now().UnixNano()
			}

			if p.IsStream {
				p.mu.RLock()
				for _, streamInfo := range p.StreamInfo {
					streamInfo.StartedAt = startedAt
				}
				p.mu.RUnlock()
			} else {
				p.FileInfo.StartedAt = startedAt
			}
			p.Info.Status = livekit.EgressStatus_EGRESS_ACTIVE
		}

	default:
		p.Logger.Debugw(msg.String())
	}

	return true
}

func (p *compositePipeline) UpdateStream(req *livekit.UpdateStreamRequest) error {
	if !p.IsStream {
		return errors.ErrInvalidRPC
	}

	now := time.Now().UnixNano()
	for _, url := range req.AddOutputUrls {
		switch p.StreamProtocol {
		case livekit.StreamProtocol_RTMP:
			if !strings.HasPrefix(url, "rtmp://") && !strings.HasPrefix(url, "rtmps://") {
				return errors.ErrInvalidUrl(url, p.StreamProtocol)
			}
		}

		if err := p.out.AddSink(url); err != nil {
			return err
		}

		streamInfo := &livekit.StreamInfo{
			Url:       url,
			StartedAt: now,
		}

		p.mu.Lock()
		p.StreamInfo[url] = streamInfo
		p.mu.Unlock()

		stream := p.Info.GetStream()
		stream.Info = append(stream.Info, streamInfo)
	}

	for _, url := range req.RemoveOutputUrls {
		if err := p.out.RemoveSink(url); err != nil {
			return err
		}

		p.mu.Lock()
		p.StreamInfo[url].EndedAt = now
		delete(p.StreamInfo, url)
		p.mu.Unlock()
	}

	return nil
}

func (p *compositePipeline) Stop() {
	select {
	case <-p.closed:
		return
	default:
		close(p.closed)
		p.Info.Status = livekit.EgressStatus_EGRESS_ENDING

		p.Logger.Debugw("sending EOS to pipeline")

		switch p.in.Source.(type) {
		case *source.SDKSource:
			p.in.Close()
		case *source.WebSource:
			p.pipeline.SendEvent(gst.NewEOSEvent())
		}
	}
}

// handleError returns true if the error has been handled, false if the pipeline should quit
func (p *compositePipeline) handleError(gErr *gst.GError) (error, bool) {
	err := errors.New(gErr.Error())

	element, reason, ok := parseDebugInfo(gErr.DebugString())
	if !ok {
		p.Logger.Errorw("failed to parse pipeline error", err, "debug", gErr.DebugString())
		return err, false
	}

	switch reason {
	case errors.GErrNoURI, errors.GErrCouldNotConnect:
		// bad URI or could not connect. Remove rtmp output
		if err := p.out.RemoveSinkByName(element); err != nil {
			p.Logger.Errorw("failed to remove sink", err)
			return err, false
		}
		p.removed[element] = true
		return err, true
	case errors.GErrFailedToStart:
		// returned after an added rtmp sink failed to start
		// should be preceded by a GErrNoURI on the same sink
		handled := p.removed[element]
		if !handled {
			p.Logger.Errorw("element failed to start", err)
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
			p.Logger.Errorw("streaming sink stopped", err)
		}
		return err, handled
	default:
		// input failure or file write failure. Fatal
		p.Logger.Errorw("pipeline error", err, "debug", gErr.DebugString())
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
