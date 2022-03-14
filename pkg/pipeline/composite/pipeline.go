package composite

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/livekit-egress/pkg/sink"
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

	// egress info
	info       *livekit.EgressInfo
	fileInfo   *livekit.FileInfo
	streamInfo map[string]*livekit.StreamInfo

	// internal
	mu             sync.RWMutex
	logger         logger.Logger
	isStream       bool
	streamProtocol livekit.StreamProtocol
	params.FileParams
	removed map[string]bool
	closed  chan struct{}
}

func NewPipeline(conf *config.Config, p *params.Params) (*compositePipeline, error) {
	if !initialized {
		gst.Init(nil)
		initialized = true
	}

	// create input bin
	in, err := newInputBin(conf, p)
	if err != nil {
		return nil, err
	}

	// create output bin
	out, err := newOutputBin(p)
	if err != nil {
		return nil, err
	}

	// link elements
	pipeline, err := buildPipeline(in, out, p.IsStream)
	if err != nil {
		return nil, err
	}

	return &compositePipeline{
		pipeline:       pipeline,
		in:             in,
		out:            out,
		info:           p.Info,
		fileInfo:       p.FileInfo,
		streamInfo:     p.StreamInfo,
		logger:         p.Logger,
		isStream:       p.IsStream,
		streamProtocol: p.StreamProtocol,
		FileParams:     p.FileParams,
		removed:        make(map[string]bool),
		closed:         make(chan struct{}),
	}, nil
}

func (p *compositePipeline) Info() *livekit.EgressInfo {
	return p.info
}

func (p *compositePipeline) Run() *livekit.EgressInfo {
	// wait until room is ready
	select {
	case <-p.closed:
		p.info.Status = livekit.EgressStatus_EGRESS_COMPLETE
		p.in.Close()
		return p.info
	case <-p.in.StartRecording():
		// continue
	}

	// close when room ends
	go func() {
		<-p.in.EndRecording()
		p.Stop()
	}()

	// add watch
	started := false
	loop := glib.NewMainLoop(glib.MainContextDefault(), false)
	p.pipeline.GetPipelineBus().AddWatch(func(msg *gst.Message) bool {
		switch msg.Type() {
		case gst.MessageEOS:
			// EOS received - close and return
			p.logger.Debugw("EOS received, stopping pipeline")
			_ = p.pipeline.BlockSetState(gst.StateNull)
			p.logger.Debugw("pipeline stopped")

			loop.Quit()
			return false
		case gst.MessageError:
			// handle error if possible, otherwise close and return
			err, handled := p.handleError(msg.ParseError())
			if !handled {
				p.info.Error = err.Error()
				loop.Quit()
				return false
			}
		case gst.MessageStateChanged:
			if !started && msg.Source() == pipelineSource {
				_, newState := msg.ParseStateChanged()
				if newState == gst.StatePlaying {
					started = true
					startedAt := time.Now().UnixNano()
					if p.isStream {
						p.mu.RLock()
						for _, streamInfo := range p.streamInfo {
							streamInfo.StartedAt = startedAt
						}
						p.mu.RUnlock()
					} else {
						p.fileInfo.StartedAt = startedAt

					}
					p.info.Status = livekit.EgressStatus_EGRESS_ACTIVE
				}
			}
		default:
			p.logger.Debugw(msg.String())
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

	// add end times to egress info
	endedAt := time.Now().UnixNano()
	if p.isStream {
		p.mu.RLock()
		for _, info := range p.streamInfo {
			info.EndedAt = endedAt
		}
		p.mu.RUnlock()
	} else {
		p.fileInfo.EndedAt = time.Now().UnixNano()
	}

	// close input source
	p.in.Close()

	// upload file
	var err error
	if !p.isStream {
		switch u := p.FileUpload.(type) {
		case *livekit.S3Upload:
			p.fileInfo.Location, err = sink.UploadS3(u, p.FileParams)
		case *livekit.GCPUpload:
			p.fileInfo.Location, err = sink.UploadGCP(u, p.FileParams)
		case *livekit.AzureBlobUpload:
			p.fileInfo.Location, err = sink.UploadAzure(u, p.FileParams)
		default:
			p.fileInfo.Location = p.Filepath
		}
	}
	if err != nil {
		p.logger.Errorw("could not upload file", err)
		p.info.Error = err.Error()
	}

	// return result
	p.info.Status = livekit.EgressStatus_EGRESS_COMPLETE
	return p.info
}

func (p *compositePipeline) UpdateStream(req *livekit.UpdateStreamRequest) error {
	if !p.isStream {
		return errors.ErrInvalidRPC
	}

	now := time.Now().UnixNano()
	for _, url := range req.AddOutputUrls {
		switch p.streamProtocol {
		case livekit.StreamProtocol_RTMP:
			if !strings.HasPrefix(url, "rtmp://") {
				return errors.ErrInvalidUrl(url, p.streamProtocol)
			}
		}

		if err := p.out.addSink(url); err != nil {
			return err
		}

		streamInfo := &livekit.StreamInfo{
			Url:       url,
			StartedAt: now,
		}

		p.mu.Lock()
		p.streamInfo[url] = streamInfo
		p.mu.Unlock()

		stream := p.info.GetStream()
		stream.Info = append(stream.Info, streamInfo)
	}

	for _, url := range req.RemoveOutputUrls {
		if err := p.out.removeSink(url); err != nil {
			return err
		}

		p.mu.Lock()
		p.streamInfo[url].EndedAt = now
		delete(p.streamInfo, url)
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
		p.info.Status = livekit.EgressStatus_EGRESS_ENDING

		p.logger.Debugw("sending EOS to pipeline")
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
	for _, streamSink := range out.sinks {
		// link queue to rtmp sink
		if err := streamSink.queue.Link(streamSink.sink); err != nil {
			return nil, err
		}

		pad := out.tee.GetRequestPad("src_%u")
		streamSink.pad = pad.GetName()

		// link tee to queue
		if linkReturn := pad.Link(streamSink.queue.GetStaticPad("sink")); linkReturn != gst.PadLinkOK {
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
		p.logger.Errorw("failed to parse pipeline error", err, "debug", gErr.DebugString())
		return err, false
	}

	switch reason {
	case errors.GErrNoURI, errors.GErrCouldNotConnect:
		// bad URI or could not connect. Remove rtmp output
		if err := p.out.removeSinkByName(element); err != nil {
			p.logger.Errorw("failed to remove sink", err)
			return err, false
		}
		p.removed[element] = true
		return err, true
	case errors.GErrFailedToStart:
		// returned after an added rtmp sink failed to start
		// should be preceded by a GErrNoURI on the same sink
		handled := p.removed[element]
		if !handled {
			p.logger.Errorw("element failed to start", err)
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
			p.logger.Errorw("streaming sink stopped", err)
		}
		return err, handled
	default:
		// input failure or file write failure. Fatal
		p.logger.Errorw("pipeline error", err, "debug", gErr.DebugString())
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
