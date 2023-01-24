package pipeline

import (
	"context"
	"regexp"
	"time"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/input/sdk"
	"github.com/livekit/egress/pkg/pipeline/input/web"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

const (
	fragmentOpenedMessage = "splitmuxsink-fragment-opened"
	fragmentClosedMessage = "splitmuxsink-fragment-closed"
	fragmentLocation      = "location"
	fragmentRunningTime   = "running-time"

	elementGstRtmp2Sink = "GstRtmp2Sink"
	elementGstAppSrc    = "GstAppSrc"
	elementSplitMuxSink = "GstSplitMuxSink"
)

func (p *Pipeline) messageWatch(msg *gst.Message) bool {
	switch msg.Type() {
	case gst.MessageEOS:
		// EOS received - close and return
		p.handleMessageEOS()
		return false

	case gst.MessageError:
		// handle error if possible, otherwise close and return
		if err := p.handleMessageError(msg.ParseError()); err != nil {
			p.Info.Error = err.Error()
			p.stop()
			return false
		}

	case gst.MessageStateChanged:
		p.handleMessageStateChanged(msg)

	case gst.MessageElement:
		if err := p.handleMessageElement(msg); err != nil {
			p.Info.Error = err.Error()
			p.stop()
			return false
		}

	default:
		logger.Debugw(msg.String())
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

// handleMessageError returns true if the error has been handled, false if the pipeline should quit
func (p *Pipeline) handleMessageError(gErr *gst.GError) error {
	element, name, message := parseDebugInfo(gErr)
	err := errors.ErrGstPipelineError(errors.New(gErr.Error()))

	switch {
	case element == elementGstRtmp2Sink:
		// bad URI or could not connect. Remove rtmp output
		url, err := p.out.GetUrlFromName(name)
		if err != nil {
			logger.Warnw("rtmp output not found", err, "url", url)
			return err
		}
		return p.removeSink(url, livekit.StreamInfo_FAILED)

	case element == elementGstAppSrc:
		if message == "streaming stopped, reason not-negotiated (-4)" {
			// send eos to app src
			logger.Debugw("streaming stopped", "name", name)
			p.in.(*sdk.SDKInput).StreamStopped(name)
			return nil
		}

	case element == elementSplitMuxSink:
		// We sometimes get GstSplitMuxSink errors if send EOS before the first media was sent to the mux
		if message == ":muxer" {
			select {
			case <-p.closed:
				logger.Debugw("GstSplitMuxSink failure after sending EOS")
				return nil
			default:
			}
		}
	}

	// input failure or file write failure. Fatal
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

	switch msg.Source() {
	case sdk.AudioAppSource, sdk.VideoAppSource:
		switch s := p.in.(type) {
		case *sdk.SDKInput:
			s.Playing(msg.Source())
		}

	case pipelineSource:
		p.playing = true
		switch s := p.in.(type) {
		case *sdk.SDKInput:
			p.updateStartTime(s.GetStartTime())
		case *web.WebInput:
			p.updateStartTime(time.Now().UnixNano())
		}
	}

	return
}

func (p *Pipeline) handleMessageElement(msg *gst.Message) error {
	s := msg.GetStructure()
	if s != nil {
		switch s.Name() {
		case fragmentOpenedMessage:
			filepath, t, err := getSegmentParamsFromGstStructure(s)
			if err != nil {
				logger.Errorw("failed to retrieve segment parameters from event", err)
				return err
			}

			logger.Debugw("fragment opened", "location", filepath, "running time", t)

			if p.playlistWriter != nil {
				if err = p.playlistWriter.StartSegment(filepath, t); err != nil {
					logger.Errorw("failed to register new segment with playlist writer", err, "location", filepath, "running time", t)
					return err
				}
			}

		case fragmentClosedMessage:
			filepath, t, err := getSegmentParamsFromGstStructure(s)
			if err != nil {
				logger.Errorw("failed to retrieve segment parameters from event", err, "location", filepath, "running time", t)
				return err
			}

			logger.Debugw("fragment closed", "location", filepath, "running time", t)

			// We need to dispatch to a queue to:
			// 1. Avoid concurrent access to the SegmentsInfo structure
			// 2. Ensure that playlists are uploaded in the same order they are enqueued to avoid an older playlist overwriting a newer one
			if err = p.enqueueSegmentUpload(filepath, t); err != nil {
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
