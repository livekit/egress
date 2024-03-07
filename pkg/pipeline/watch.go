// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/pipeline/sink"
	"github.com/livekit/egress/pkg/pipeline/source"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

const (
	// watch errors
	msgClockProblem           = "GStreamer error: clock problem."
	msgStreamingNotNegotiated = "streaming stopped, reason not-negotiated (-4)"
	msgMuxer                  = ":muxer"

	elementGstRtmp2Sink = "GstRtmp2Sink"
	elementGstAppSrc    = "GstAppSrc"
	elementSplitMuxSink = "GstSplitMuxSink"

	// watch elements
	msgFirstSampleMetadata = "FirstSampleMetadata"
	msgFragmentOpened      = "splitmuxsink-fragment-opened"
	msgFragmentClosed      = "splitmuxsink-fragment-closed"
	msgGstMultiFileSink    = "GstMultiFileSink"

	fragmentLocation    = "location"
	fragmentRunningTime = "running-time"

	gstMultiFileSinkFilename  = "filename"
	gstMultiFileSinkTimestamp = "timestamp"

	// common gst errors
	msgWrongThread = "Called from wrong thread"

	// common gst warnings
	msgKeyframe         = "Could not request a keyframe. Files may not split at the exact location they should"
	msgLatencyQuery     = "Latency query failed"
	msgTaps             = "can't find exact taps"
	msgInputDisappeared = "Can't copy metadata because input buffer disappeared"

	// common gst fixmes
	msgStreamStart       = "stream-start event without group-id. Consider implementing group-id handling in the upstream elements"
	msgCreatingStream    = "Creating random stream-id, consider implementing a deterministic way of creating a stream-id"
	msgAggregateSubclass = "Subclass should call gst_aggregator_selected_samples() from its aggregate implementation."
)

func (c *Controller) gstLog(level gst.DebugLevel, file, function string, line int, _ *glib.Object, message string) {
	var lvl string
	switch level {
	case gst.LevelNone:
		lvl = "none"
	case gst.LevelError:
		switch message {
		case msgWrongThread:
			// ignore
			return
		default:
			lvl = "error"
		}
	case gst.LevelWarning:
		switch message {
		case msgKeyframe, msgLatencyQuery, msgTaps, msgInputDisappeared:
			// ignore
			return
		default:
			lvl = "warning"
		}
	case gst.LevelFixMe:
		switch message {
		case msgStreamStart, msgCreatingStream, msgAggregateSubclass:
			// ignore
			return
		default:
			lvl = "fixme"
		}
	case gst.LevelInfo:
		lvl = "info"
	case gst.LevelDebug:
		lvl = "debug"
	default:
		lvl = "log"
	}

	var msg string
	if function != "" {
		msg = fmt.Sprintf("[gst %s] %s: %s", lvl, function, message)
	} else {
		msg = fmt.Sprintf("[gst %s] %s", lvl, message)
	}
	args := []interface{}{"caller", fmt.Sprintf("%s:%d", file, line)}
	c.gstLogger.Debugw(msg, args...)
}

func (c *Controller) messageWatch(msg *gst.Message) bool {
	var err error
	switch msg.Type() {
	case gst.MessageEOS:
		logger.Infow("EOS received")
		if c.eosTimer != nil {
			c.eosTimer.Stop()
		}
		c.p.Stop()
		return false
	case gst.MessageWarning:
		err = c.handleMessageWarning(msg.ParseWarning())
	case gst.MessageError:
		err = c.handleMessageError(msg.ParseError())
	case gst.MessageStateChanged:
		c.handleMessageStateChanged(msg)
	case gst.MessageElement:
		err = c.handleMessageElement(msg)
	}
	if err != nil {
		if c.Debug.EnableProfiling {
			c.uploadDebugFiles()
		}
		c.OnError(err)
		return false
	}

	return true
}

func (c *Controller) handleMessageWarning(gErr *gst.GError) error {
	element, _, message := parseDebugInfo(gErr)

	if gErr.Message() == msgClockProblem {
		err := errors.ErrGstPipelineError(gErr)
		logger.Errorw(gErr.Error(), errors.New(message), "element", element)
		return err
	}

	logger.Warnw(gErr.Message(), errors.New(message), "element", element)
	return nil
}

// handleMessageError returns true if the error has been handled, false if the pipeline should quit
func (c *Controller) handleMessageError(gErr *gst.GError) error {
	element, name, message := parseDebugInfo(gErr)

	switch {
	case element == elementGstRtmp2Sink:
		name = strings.Split(name, "_")[1]

		if !c.eos.IsBroken() {
			// try reconnecting
			ok, err := c.streamBin.MaybeResetStream(name, gErr)
			if err != nil {
				logger.Errorw("failed to reset stream", err)
			} else if ok {
				return nil
			}
		}

		// remove sink
		url, err := c.streamBin.GetStreamUrl(name)
		if err != nil {
			logger.Warnw("rtmp output not found", err, "url", url)
			return err
		}

		return c.removeSink(context.Background(), url, gErr)

	case element == elementGstAppSrc:
		if message == msgStreamingNotNegotiated {
			// send eos to app src
			logger.Debugw("streaming stopped", "name", name)
			c.src.(*source.SDKSource).StreamStopped(name)
			return nil
		}

	case element == elementSplitMuxSink:
		// We sometimes get GstSplitMuxSink errors if send EOS before the first media was sent to the mux
		if message == msgMuxer {
			if c.eos.IsBroken() {
				logger.Debugw("GstSplitMuxSink failure after sending EOS")
				return nil
			}
		}
	}

	// input failure or file write failure. Fatal
	err := errors.ErrGstPipelineError(gErr)
	logger.Errorw(gErr.Error(), errors.New(message), "element", name)
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

func (c *Controller) handleMessageStateChanged(msg *gst.Message) {
	_, newState := msg.ParseStateChanged()
	if newState != gst.StatePlaying {
		return
	}

	s := msg.Source()
	if s == pipelineName {
		c.playing.Once(func() {
			logger.Infow("pipeline playing")
			c.updateStartTime(c.src.GetStartedAt())
		})
	} else if strings.HasPrefix(s, "app_") {
		s = s[4:]
		logger.Infow(fmt.Sprintf("%s playing", s))
		c.src.(*source.SDKSource).Playing(s)
	}

	return
}

func (c *Controller) handleMessageElement(msg *gst.Message) error {
	s := msg.GetStructure()
	if s != nil {
		switch s.Name() {
		case msgFirstSampleMetadata:
			startDate, err := getFirstSampleMetadataFromGstStructure(s)
			if err != nil {
				return err
			}
			logger.Debugw("received FirstSampleMetadata message", "startDate", startDate)

			c.getSegmentSink().UpdateStartDate(startDate)

		case msgFragmentOpened:
			filepath, t, err := getSegmentParamsFromGstStructure(s)
			if err != nil {
				logger.Errorw("failed to retrieve segment parameters from event", err)
				return err
			}

			if err = c.getSegmentSink().FragmentOpened(filepath, t); err != nil {
				logger.Errorw("failed to register new segment with playlist writer", err, "location", filepath, "runningTime", t)
				return err
			}

		case msgFragmentClosed:
			filepath, t, err := getSegmentParamsFromGstStructure(s)
			if err != nil {
				logger.Errorw("failed to retrieve segment parameters from event", err, "location", filepath, "runningTime", t)
				return err
			}

			// We need to dispatch to a queue to:
			// 1. Avoid concurrent access to the SegmentsInfo structure
			// 2. Ensure that playlists are uploaded in the same order they are enqueued to avoid an older playlist overwriting a newer one
			if err = c.getSegmentSink().FragmentClosed(filepath, t); err != nil {
				logger.Errorw("failed to end segment with playlist writer", err, "runningTime", t)
				return err
			}

		case msgGstMultiFileSink:
			location, ts, err := getImageInformationFromGstStructure(s)
			if err != nil {
				return err
			}
			logger.Debugw("received GstMultiFileSink message", "location", location, "timestamp", ts, "source", msg.Source())

			imageSink := c.getImageSink(msg.Source())
			if imageSink == nil {
				return errors.ErrSinkNotFound
			}

			err = imageSink.NewImage(location, ts)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func getSegmentParamsFromGstStructure(s *gst.Structure) (filepath string, time uint64, err error) {
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

	return filepath, ti, nil
}

func getFirstSampleMetadataFromGstStructure(s *gst.Structure) (startDate time.Time, err error) {
	firstSampleMetadata := builder.FirstSampleMetadata{}
	err = s.UnmarshalInto(&firstSampleMetadata)
	if err != nil {
		return time.Time{}, err
	}

	return time.Unix(0, firstSampleMetadata.StartDate), nil
}

func getImageInformationFromGstStructure(s *gst.Structure) (string, uint64, error) {
	loc, err := s.GetValue(gstMultiFileSinkFilename)
	if err != nil {
		return "", 0, err
	}
	filepath, ok := loc.(string)
	if !ok {
		return "", 0, errors.ErrGstPipelineError(errors.New("invalid type for location"))
	}

	t, err := s.GetValue(gstMultiFileSinkTimestamp)
	if err != nil {
		return "", 0, err
	}
	ti, ok := t.(uint64)
	if !ok {
		return "", 0, errors.ErrGstPipelineError(errors.New("invalid type for time"))
	}

	return filepath, ti, nil

}

func (c *Controller) getSegmentSink() *sink.SegmentSink {
	s := c.sinks[types.EgressTypeSegments]
	if len(s) == 0 {
		return nil
	}

	return s[0].(*sink.SegmentSink)
}

func (c *Controller) getImageSink(name string) *sink.ImageSink {
	id := name[len("multifilesink_"):]

	s := c.sinks[types.EgressTypeImages]
	if len(s) == 0 {
		return nil
	}

	// Use a map here?
	for _, si := range s {
		if si.(*sink.ImageSink).Id == id {
			return si.(*sink.ImageSink)
		}
	}

	return nil
}
