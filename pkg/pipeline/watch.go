package pipeline

import (
	"context"
	"regexp"
	"time"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/sink"
	"github.com/livekit/egress/pkg/pipeline/source"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

const (
	msgClockProblem     = "GStreamer error: clock problem."
	msgStreamingStopped = "streaming stopped, reason not-negotiated (-4)"
	msgMuxer            = ":muxer"
	msgFragmentOpened   = "splitmuxsink-fragment-opened"
	msgFragmentClosed   = "splitmuxsink-fragment-closed"

	fragmentLocation    = "location"
	fragmentRunningTime = "running-time"

	elementGstRtmp2Sink = "GstRtmp2Sink"
	elementGstAppSrc    = "GstAppSrc"
	elementSplitMuxSink = "GstSplitMuxSink"
)

func (p *Pipeline) messageWatch(msg *gst.Message) bool {
	var err error
	switch msg.Type() {
	case gst.MessageEOS:
		p.handleMessageEOS()
		return false

	case gst.MessageWarning:
		err = p.handleMessageWarning(msg.ParseWarning())

	case gst.MessageError:
		err = p.handleMessageError(msg.ParseError())

	case gst.MessageStateChanged:
		p.handleMessageStateChanged(msg)

	case gst.MessageElement:
		err = p.handleMessageElement(msg)

	default:
		logger.Debugw(msg.String(), "messageType", msg.Type().String())
	}

	if err != nil {
		p.Info.Error = err.Error()
		p.stop()
		return false
	}

	return true
}

func (p *Pipeline) handleMessageEOS() {
	if p.eosTimer != nil {
		p.eosTimer.Stop()
	}

	logger.Debugw("EOS received, stopping pipeline")
	p.closeOnce.Do(func() {
		p.close(context.Background())
	})

	p.stop()
}

func (p *Pipeline) handleMessageWarning(gErr *gst.GError) error {
	if gErr.Message() == msgClockProblem {
		err := errors.ErrGstPipelineError(gErr)
		logger.Errorw("pipeline error", err)
		return err
	}

	_, _, message := parseDebugInfo(gErr)
	logger.Warnw(gErr.Message(), errors.New(message))
	return nil
}

// handleMessageError returns true if the error has been handled, false if the pipeline should quit
func (p *Pipeline) handleMessageError(gErr *gst.GError) error {
	element, name, message := parseDebugInfo(gErr)

	switch {
	case element == elementGstRtmp2Sink:
		// bad URI or could not connect. Remove rtmp output
		url, err := p.out.GetStreamUrl(name)
		if err != nil {
			logger.Warnw("rtmp output not found", err, "url", url)
			return err
		}
		return p.removeSink(url, livekit.StreamInfo_FAILED)

	case element == elementGstAppSrc:
		if message == msgStreamingStopped {
			// send eos to app src
			logger.Debugw("streaming stopped", "name", name)
			p.src.(*source.SDKSource).StreamStopped(name)
			return nil
		}

	case element == elementSplitMuxSink:
		// We sometimes get GstSplitMuxSink errors if send EOS before the first media was sent to the mux
		if message == msgMuxer {
			select {
			case <-p.closed:
				logger.Debugw("GstSplitMuxSink failure after sending EOS")
				return nil
			default:
			}
		}
	}

	// input failure or file write failure. Fatal
	err := errors.ErrGstPipelineError(gErr)
	logger.Errorw("pipeline error", err, "element", element, "message", message)
	return err
}

// Debug info comes in the following format:
// file.c(line): method_name (): /GstPipeline:pipeline/GstBin:bin_name/GstElement:element_name:\nError message
var regExp = regexp.MustCompile("(?s)(.*?)GstPipeline:pipeline\\/GstBin:(.*?)\\/(.*?):([^:]*)(:\n)?(.*)")

func parseDebugInfo(gErr *gst.GError) (element, name, message string) {
	match := regExp.FindStringSubmatch(gErr.DebugString())

	element = match[3]
	name = match[4]
	message = match[6]
	return
}

func (p *Pipeline) handleMessageStateChanged(msg *gst.Message) {
	if p.playing {
		return
	}

	_, newState := msg.ParseStateChanged()
	if newState != gst.StatePlaying {
		return
	}

	switch s := msg.Source(); s {
	case source.AudioAppSource, source.VideoAppSource:
		p.src.(*source.SDKSource).Playing(s)

	case pipelineSource:
		p.playing = true
		switch p.SourceType {
		case types.SourceTypeSDK:
			p.updateStartTime(p.src.(*source.SDKSource).GetStartTime())
		case types.SourceTypeWeb:
			p.updateStartTime(time.Now().UnixNano())
		}
	}

	return
}

func (p *Pipeline) handleMessageElement(msg *gst.Message) error {
	s := msg.GetStructure()
	if s != nil {
		switch s.Name() {
		case msgFragmentOpened:
			filepath, t, err := getSegmentParamsFromGstStructure(s)
			if err != nil {
				logger.Errorw("failed to retrieve segment parameters from event", err)
				return err
			}

			logger.Debugw("fragment opened", "location", filepath, "running time", t)
			if err = p.getSegmentSink().StartSegment(filepath, t); err != nil {
				logger.Errorw("failed to register new segment with playlist writer", err, "location", filepath, "running time", t)
				return err
			}

		case msgFragmentClosed:
			filepath, t, err := getSegmentParamsFromGstStructure(s)
			if err != nil {
				logger.Errorw("failed to retrieve segment parameters from event", err, "location", filepath, "running time", t)
				return err
			}

			logger.Debugw("fragment closed", "location", filepath, "running time", t)

			// We need to dispatch to a queue to:
			// 1. Avoid concurrent access to the SegmentsInfo structure
			// 2. Ensure that playlists are uploaded in the same order they are enqueued to avoid an older playlist overwriting a newer one
			if err = p.getSegmentSink().EnqueueSegmentUpload(filepath, t); err != nil {
				logger.Errorw("failed to end segment with playlist writer", err, "running time", t)
				return err
			}
		}
	}

	return nil
}

func getSegmentParamsFromGstStructure(s *gst.Structure) (filepath string, time int64, err error) {
	loc, err := s.GetValue(fragmentLocation)
	if err != nil {
		return "", 0, err
	}
	filepath, ok := loc.(string)
	if !ok {
		return "", 0, errors.ErrGstPipelineError(errors.New("invalid type for location"))
	}

	t, err := s.GetValue(fragmentRunningTime)
	if err != nil {
		return "", 0, err
	}
	ti, ok := t.(uint64)
	if !ok {
		return "", 0, errors.ErrGstPipelineError(errors.New("invalid type for time"))
	}

	return filepath, int64(ti), nil
}

func (p *Pipeline) getSegmentSink() *sink.SegmentSink {
	return p.sinks[types.EgressTypeSegments].(*sink.SegmentSink)
}
