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

	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/pipeline/source"
)

const (
	// noisy gst errors
	msgWrongThread = "Called from wrong thread"

	// noisy gst warnings
	msgKeyframe                    = "Could not request a keyframe. Files may not split at the exact location they should"
	msgLatencyQuery                = "Latency query failed"
	msgTaps                        = "can't find exact taps"
	msgInputDisappeared            = "Can't copy metadata because input buffer disappeared"
	msgSkippingSegment             = "error reading data -1 (reason: Success), skipping segment"
	fnGstAudioResampleCheckDiscont = "gst_audio_resample_check_discont"

	// noisy gst fixmes
	msgStreamStart       = "stream-start event without group-id. Consider implementing group-id handling in the upstream elements"
	msgCreatingStream    = "Creating random stream-id, consider implementing a deterministic way of creating a stream-id"
	msgAggregateSubclass = "Subclass should call gst_aggregator_selected_samples() from its aggregate implementation."

	// rtmp client
	catRtmpClient      = "rtmpclient"
	fnSendCreateStream = "send_create_stream"
)

var (
	logLevels = map[gst.DebugLevel]string{
		gst.LevelError:   "error",
		gst.LevelWarning: "warning",
		gst.LevelFixMe:   "fixme",
		gst.LevelInfo:    "info",
		gst.LevelDebug:   "debug",
		gst.LevelLog:     "log",
		gst.LevelTrace:   "trace",
		gst.LevelMemDump: "memdump",
	}

	ignore = map[string]bool{
		msgWrongThread:                 true,
		msgKeyframe:                    true,
		msgLatencyQuery:                true,
		msgTaps:                        true,
		msgInputDisappeared:            true,
		msgSkippingSegment:             true,
		fnGstAudioResampleCheckDiscont: true,
		msgStreamStart:                 true,
		msgCreatingStream:              true,
		msgAggregateSubclass:           true,
	}
)

func (c *Controller) gstLog(
	cat *gst.DebugCategory,
	level gst.DebugLevel,
	file, function string, line int,
	_ *gst.LoggedObject,
	debugMsg *gst.DebugMessage,
) {
	category := cat.GetName()
	message := debugMsg.Get()
	lvl, ok := logLevels[level]
	if !ok || ignore[message] || ignore[function] {
		return
	}

	if category == catRtmpClient {
		if function == fnSendCreateStream {
			streamID := strings.Split(message, "'")[1]
			c.updateStreamStartTime(streamID)
		}
		return
	}

	var msg string
	if function != "" {
		msg = fmt.Sprintf("[%s %s] %s: %s", category, lvl, function, message)
	} else {
		msg = fmt.Sprintf("[%s %s] %s", category, lvl, message)
	}
	c.gstLogger.Debugw(msg, "caller", fmt.Sprintf("%s:%d", file, line))
}

func (c *Controller) messageWatch(msg *gst.Message) bool {
	var err error
	switch msg.Type() {
	case gst.MessageEOS:
		logger.Infow("pipeline received EOS")
		if c.eosTimer != nil {
			c.eosTimer.Stop()
		}
		c.eosReceived.Break()
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
	case gst.MessageQoS:
		c.handleMessageQoS(msg)
	}
	if err != nil {
		c.OnError(err)
		return false
	}

	return true
}

const (
	msgClockProblem = "GStreamer error: clock problem."
)

func (c *Controller) handleMessageWarning(gErr *gst.GError) error {
	element, name, message := parseDebugInfo(gErr)

	if gErr.Message() == msgClockProblem {
		err := errors.ErrGstPipelineError(gErr)
		logger.Errorw(gErr.Error(), errors.New(message), "element", element)
		return err
	}

	if element == elementGstSrtSink {
		streamName := strings.Split(name, "_")[1]
		stream, err := c.getStreamSink().GetStream(streamName)
		if err != nil {
			return err
		}

		return c.streamFailed(context.Background(), stream, gErr)
	}

	logger.Warnw(gErr.Message(), errors.New(message), "element", element)
	return nil
}

const (
	elementGstAppSrc       = "GstAppSrc"
	elementGstRtmp2Sink    = "GstRtmp2Sink"
	elementGstSplitMuxSink = "GstSplitMuxSink"
	elementGstSrtSink      = "GstSRTSink"

	msgStreamingNotNegotiated = "streaming stopped, reason not-negotiated (-4)"
	msgMuxer                  = ":muxer"
)

// handleMessageError returns true if the error has been handled, false if the pipeline should quit
func (c *Controller) handleMessageError(gErr *gst.GError) error {
	element, name, message := parseDebugInfo(gErr)

	switch {
	case element == elementGstRtmp2Sink:
		streamSink := c.getStreamSink()

		streamName := strings.Split(name, "_")[1]
		stream, err := streamSink.GetStream(streamName)
		if err != nil {
			return err
		}

		if !c.eosSent.IsBroken() {
			// try reconnecting
			ok, err := streamSink.ResetStream(stream, gErr)
			if err != nil {
				logger.Errorw("failed to reset stream", err)
			} else if ok {
				c.trackStreamRetry(context.Background(), stream)
				return nil
			}
		}

		// remove sink
		return c.streamFailed(context.Background(), stream, gErr)

	case element == elementGstSrtSink:
		streamName := strings.Split(name, "_")[1]
		stream, err := c.getStreamSink().GetStream(streamName)
		if err != nil {
			return err
		}

		return c.streamFailed(context.Background(), stream, gErr)

	case element == elementGstAppSrc:
		if message == msgStreamingNotNegotiated {
			// send eosSent to app src
			logger.Debugw("streaming stopped", "name", name)
			c.src.(*source.SDKSource).StreamStopped(name)
			return nil
		}

	case element == elementGstSplitMuxSink:
		// We sometimes get GstSplitMuxSink errors if EOS was received before any data
		if message == msgMuxer {
			if c.eosSent.IsBroken() {
				logger.Debugw("GstSplitMuxSink failure after sending EOS")
				return nil
			}
		}
	}

	// input failure or file write failure. Fatal
	err := errors.ErrGstPipelineError(gErr)
	logger.Errorw(gErr.Error(), errors.New(message), "element", element, "name", name)
	return err
}

func (c *Controller) handleMessageStateChanged(msg *gst.Message) {
	_, newState := msg.ParseStateChanged()
	s := msg.Source()
	if s == pipelineName {
		if newState == gst.StatePaused {
			c.paused.Once(func() {
				logger.Infow("pipeline paused")
				c.callbacks.OnPipelinePaused()
			})
		}
		if newState == gst.StatePlaying {
			c.playing.Once(func() {
				var timeToPlaying time.Duration

				if !c.pipelineCreatedAt.IsZero() {
					timeToPlaying = time.Since(c.pipelineCreatedAt)
				}

				logger.Infow("pipeline playing", "timeToPlaying", timeToPlaying)
				c.updateStartTime(c.src.GetStartedAt())
			})
		}
		return
	}

	if newState != gst.StatePlaying {
		return
	}

	if strings.HasPrefix(s, "app_") {
		trackID := s[4:]
		logger.Infow(fmt.Sprintf("%s playing", trackID))
		c.src.(*source.SDKSource).Playing(trackID)
	}
}

const (
	msgFirstSampleMetadata = "FirstSampleMetadata"
	msgFragmentOpened      = "splitmuxsink-fragment-opened"
	msgFragmentClosed      = "splitmuxsink-fragment-closed"
	msgGstMultiFileSink    = "GstMultiFileSink"
)

func (c *Controller) handleMessageElement(msg *gst.Message) error {
	s := msg.GetStructure()
	if s != nil {
		switch s.Name() {
		case builder.LeakyQueueStatsMessage:
			queueName, dropped, err := parseLeakyQueueStats(s)
			if err != nil {
				logger.Debugw("failed to parse leaky queue stats message", err)
				return nil
			}
			c.stats.droppedVideoBuffers.Add(dropped)
			c.stats.droppedVideoBuffersByQueue[queueName] = dropped

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

func parseLeakyQueueStats(s *gst.Structure) (queue string, dropped uint64, err error) {
	queueValue, err := s.GetValue("queue")
	if err != nil {
		return "", 0, err
	}
	queue, _ = queueValue.(string)

	droppedValue, err := s.GetValue("dropped")
	if err != nil {
		return queue, 0, err
	}
	dropped = normalizeUint64(droppedValue)
	return queue, dropped, nil
}

func normalizeUint64(value interface{}) uint64 {
	switch v := value.(type) {
	case uint64:
		return v
	case uint:
		return uint64(v)
	case uint32:
		return uint64(v)
	case int:
		if v > 0 {
			return uint64(v)
		}
	case int64:
		if v > 0 {
			return uint64(v)
		}
	case int32:
		if v > 0 {
			return uint64(v)
		}
	}
	return 0
}

func (c *Controller) handleMessageQoS(msg *gst.Message) {
	if isQosForAudioMixer(msg) {
		qos := msg.ParseQoS()
		if qos == nil {
			logger.Debugw("failed to parse audio mixer QoS message")
			return
		}
		c.handleAudioMixerQoS(qos)
		return
	}
}

func (c *Controller) handleAudioMixerQoS(qosValues *gst.QoSValues) {
	c.stats.droppedAudioBuffers.Inc()
	c.stats.droppedAudioDuration.Add(qosValues.Duration)
}

// Debug info comes in the following format:
// file.c(line): method_name (): /GstPipeline:pipeline/GstBin:bin_name/GstElement:element_name:\nError message
var gstDebug = regexp.MustCompile("(?s)(.*?)GstPipeline:pipeline/GstBin:(.*?)/(.*?):([^:]*)(:\n)?(.*)")

func parseDebugInfo(gErr *gst.GError) (element, name, message string) {
	match := gstDebug.FindStringSubmatch(gErr.DebugString())

	if len(match) == 0 {
		return
	}

	element = match[3]
	name = match[4]
	message = match[6]
	return
}

const (
	fragmentLocation    = "location"
	fragmentRunningTime = "running-time"
)

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

const (
	gstMultiFileSinkFilename  = "filename"
	gstMultiFileSinkTimestamp = "timestamp"
)

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

func isQosForAudioMixer(msg *gst.Message) bool {
	src := msg.SourceObject()
	if src == nil {
		return false
	}

	srcName := src.GetName()
	parent := src.GetParent()

	var parentName string
	if parent != nil {
		parentName = parent.GetName()
	}

	// a bit brittle as it relies on mixer name not being changed
	return strings.HasPrefix(srcName, "sink_") && strings.HasPrefix(parentName, "audiomixer")
}
