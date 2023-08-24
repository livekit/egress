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

	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"

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

	fragmentLocation    = "location"
	fragmentRunningTime = "running-time"

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

func (c *Controller) gstLog(level gst.DebugLevel, file, function string, line int, obj *glib.Object, message string) {
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
	if obj != nil {
		name, err := obj.GetProperty("name")
		if err == nil {
			args = append(args, "object", name.(string))
		}
	}
	c.gstLogger.Debugw(msg, args...)
}

func (c *Controller) messageWatch(msg *gst.Message) bool {
	var err error
	switch msg.Type() {
	case gst.MessageEOS:
		_ = c.OnEOS()

		logger.Infow("EOS received, stopping pipeline")
		c.Stop()
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
		if strings.HasPrefix(gErr.Error(), "Connection error") && !c.eosSent.IsBroken() {
			// try reconnecting
			ok, err := c.streamBin.ResetStream(name, gErr)
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
			if c.eosSent.IsBroken() {
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
	if c.playing.IsBroken() {
		return
	}

	_, newState := msg.ParseStateChanged()
	if newState != gst.StatePlaying {
		return
	}

	s := msg.Source()
	if s == pipelineSource {
		logger.Infow("pipeline playing")

		c.playing.Break()
		switch c.SourceType {
		case types.SourceTypeSDK:
			c.updateStartTime(c.src.(*source.SDKSource).GetStartTime())
		case types.SourceTypeWeb:
			c.updateStartTime(time.Now().UnixNano())
		}
	} else if strings.HasPrefix(s, "TR_") {
		logger.Infow(fmt.Sprintf("%s playing", s))
		c.src.(*source.SDKSource).Playing(s)
	}

	return
}

func (c *Controller) handleMessageElement(msg *gst.Message) error {
	s := msg.GetStructure()
	if s != nil {
		switch s.Name() {
		case msgFragmentOpened:
			if timer := c.eosTimer; timer != nil {
				timer.Reset(eosTimeout)
			}

			filepath, t, err := getSegmentParamsFromGstStructure(s)
			if err != nil {
				logger.Errorw("failed to retrieve segment parameters from event", err)
				return err
			}

			if err = c.getSegmentSink().StartSegment(filepath, t); err != nil {
				logger.Errorw("failed to register new segment with playlist writer", err, "location", filepath, "runningTime", t)
				return err
			}

		case msgFragmentClosed:
			if timer := c.eosTimer; timer != nil {
				timer.Reset(eosTimeout)
			}

			filepath, t, err := getSegmentParamsFromGstStructure(s)
			if err != nil {
				logger.Errorw("failed to retrieve segment parameters from event", err, "location", filepath, "runningTime", t)
				return err
			}

			logger.Debugw("fragment closed", "location", filepath, "runningTime", t)

			// We need to dispatch to a queue to:
			// 1. Avoid concurrent access to the SegmentsInfo structure
			// 2. Ensure that playlists are uploaded in the same order they are enqueued to avoid an older playlist overwriting a newer one
			if err = c.getSegmentSink().EnqueueSegmentUpload(filepath, t); err != nil {
				logger.Errorw("failed to end segment with playlist writer", err, "runningTime", t)
				return err
			}

		case msgFirstSampleMetadata:
			startDate, err := getFirstSampleMetadataFromGstStructure(s)
			if err != nil {
				return err
			}
			logger.Debugw("received FirstSampleMetadata message", "startDate", startDate)

			c.getSegmentSink().UpdateStartDate(startDate)
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

func getFirstSampleMetadataFromGstStructure(s *gst.Structure) (startDate time.Time, err error) {
	firstSampleMetadata := builder.FirstSampleMetadata{}
	err = s.UnmarshalInto(&firstSampleMetadata)
	if err != nil {
		return time.Time{}, err
	}

	return time.Unix(0, firstSampleMetadata.StartDate), nil
}

func (c *Controller) getSegmentSink() *sink.SegmentSink {
	return c.sinks[types.EgressTypeSegments].(*sink.SegmentSink)
}
