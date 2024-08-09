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
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"go.uber.org/zap"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/info"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/pipeline/sink"
	"github.com/livekit/egress/pkg/pipeline/source"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

const (
	pipelineName = "pipeline"
)

type Controller struct {
	*config.PipelineConfig
	ipcServiceClient ipc.EgressServiceClient

	// gstreamer
	gstLogger *zap.SugaredLogger
	src       source.Source
	p         *gstreamer.Pipeline
	sinks     map[types.EgressType][]sink.Sink
	streamBin *builder.StreamBin
	callbacks *gstreamer.Callbacks

	// internal
	mu         sync.Mutex
	monitor    *stats.HandlerMonitor
	limitTimer *time.Timer
	playing    core.Fuse
	eos        core.Fuse
	eosTimer   *time.Timer
	stopped    core.Fuse
}

func New(ctx context.Context, conf *config.PipelineConfig, ipcServiceClient ipc.EgressServiceClient) (*Controller, error) {
	ctx, span := tracer.Start(ctx, "Pipeline.New")
	defer span.End()

	var err error
	c := &Controller{
		PipelineConfig:   conf,
		ipcServiceClient: ipcServiceClient,
		gstLogger:        logger.GetLogger().(logger.ZapLogger).ToZap().WithOptions(zap.WithCaller(false)),
		callbacks: &gstreamer.Callbacks{
			GstReady:   make(chan struct{}),
			BuildReady: make(chan struct{}),
		},
		monitor: stats.NewHandlerMonitor(conf.NodeID, conf.ClusterID, conf.Info.EgressId),
	}
	c.callbacks.SetOnError(c.OnError)

	// initialize gst
	go func() {
		_, span := tracer.Start(ctx, "gst.Init")
		defer span.End()
		gst.Init(nil)
		gst.SetLogFunction(c.gstLog)
		close(c.callbacks.GstReady)
	}()

	// create source
	c.src, err = source.New(ctx, conf, c.callbacks)
	if err != nil {
		return nil, err
	}

	// create sinks
	c.sinks, err = sink.CreateSinks(conf, c.callbacks, c.monitor)
	if err != nil {
		c.src.Close()
		return nil, err
	}

	// create pipeline
	<-c.callbacks.GstReady
	if err = c.BuildPipeline(); err != nil {
		c.src.Close()
		return nil, err
	}

	return c, nil
}

func (c *Controller) BuildPipeline() error {
	p, err := gstreamer.NewPipeline(pipelineName, config.Latency, c.callbacks)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	p.SetWatch(c.messageWatch)
	p.AddOnStop(func() error {
		c.stopped.Break()
		return nil
	})
	if c.SourceType == types.SourceTypeSDK {
		p.SetEOSFunc(func() bool {
			c.src.(*source.SDKSource).CloseWriters()
			return true
		})
	}

	if c.AudioEnabled {
		if err = builder.BuildAudioBin(p, c.PipelineConfig); err != nil {
			return err
		}
	}
	if c.VideoEnabled {
		if err = builder.BuildVideoBin(p, c.PipelineConfig); err != nil {
			return err
		}
	}

	var sinkBins []*gstreamer.Bin
	for egressType := range c.Outputs {
		switch egressType {
		case types.EgressTypeFile:
			var sinkBin *gstreamer.Bin
			sinkBin, err = builder.BuildFileBin(p, c.PipelineConfig)
			sinkBins = append(sinkBins, sinkBin)

		case types.EgressTypeSegments:
			var sinkBin *gstreamer.Bin
			sinkBin, err = builder.BuildSegmentBin(p, c.PipelineConfig)
			sinkBins = append(sinkBins, sinkBin)

		case types.EgressTypeStream:
			var sinkBin *gstreamer.Bin
			c.streamBin, sinkBin, err = builder.BuildStreamBin(p, c.PipelineConfig)
			sinkBins = append(sinkBins, sinkBin)

		case types.EgressTypeWebsocket:
			var sinkBin *gstreamer.Bin
			writer := c.sinks[egressType][0].(*sink.WebsocketSink)
			sinkBin, err = builder.BuildWebsocketBin(p, writer.SinkCallbacks())
			sinkBins = append(sinkBins, sinkBin)

		case types.EgressTypeImages:
			var bins []*gstreamer.Bin
			bins, err = builder.BuildImageBins(p, c.PipelineConfig)
			sinkBins = append(sinkBins, bins...)
		}
		if err != nil {
			return err
		}
	}

	for _, bin := range sinkBins {
		if err = p.AddSinkBin(bin); err != nil {
			return err
		}
	}

	if err = p.Link(); err != nil {
		return err
	}

	c.p = p
	close(c.callbacks.BuildReady)
	return nil
}

func (c *Controller) Run(ctx context.Context) *info.EgressInfo {
	ctx, span := tracer.Start(ctx, "Pipeline.Run")
	defer span.End()

	c.Info.StartedAt = time.Now().UnixNano()
	defer c.Close()

	// session limit timer
	c.startSessionLimitTimer(ctx)

	// close when room ends
	go func() {
		<-c.src.EndRecording()
		c.SendEOS(ctx, "source closed")
	}()

	// wait until room is ready
	start := c.src.StartRecording()
	if start != nil {
		logger.Debugw("waiting for start signal")
		select {
		case <-c.stopped.Watch():
			c.src.Close()
			c.Info.SetAborted(info.MsgStartNotReceived)
			return c.Info
		case <-start:
			// continue
		}
	}

	for _, si := range c.sinks {
		for _, s := range si {
			if err := s.Start(); err != nil {
				c.src.Close()
				c.Info.SetFailed(err)
				return c.Info
			}
		}
	}

	if err := c.p.Run(); err != nil {
		c.src.Close()
		c.Info.SetFailed(err)
		return c.Info
	}

	logger.Debugw("closing source")
	c.src.Close()

	logger.Debugw("closing sinks")
	for _, si := range c.sinks {
		for _, s := range si {
			if err := s.Close(); err != nil && c.playing.IsBroken() {
				c.Info.SetFailed(err)
				return c.Info
			}
		}
	}

	return c.Info
}

func (c *Controller) UpdateStream(ctx context.Context, req *livekit.UpdateStreamRequest) error {
	ctx, span := tracer.Start(ctx, "Pipeline.UpdateStream")
	defer span.End()

	o := c.GetStreamConfig()
	if o == nil {
		return errors.ErrNonStreamingPipeline
	}

	errs := errors.ErrArray{}

	// add stream outputs first
	for _, rawUrl := range req.AddOutputUrls {
		// validate and redact url
		stream, err := o.AddStream(rawUrl, o.OutputType)
		if err != nil {
			errs.AppendErr(err)
			continue
		}

		// add stream info to results
		c.mu.Lock()
		c.Info.StreamResults = append(c.Info.StreamResults, stream.StreamInfo)
		if list := (*livekit.EgressInfo)(c.Info).GetStream(); list != nil {
			list.Info = append(list.Info, stream.StreamInfo)
		}
		c.mu.Unlock()

		// add stream
		if err = c.streamBin.AddStream(stream); err != nil {
			stream.StreamInfo.Status = livekit.StreamInfo_FAILED
			stream.StreamInfo.Error = err.Error()
			stream.UpdateEndTime(time.Now().UnixNano())
			errs.AppendErr(err)
			continue
		}

		c.OutputCount.Inc()
	}

	// remove stream outputs
	for _, rawUrl := range req.RemoveOutputUrls {
		stream, err := o.GetStream(rawUrl)
		if err != nil {
			errs.AppendErr(err)
			continue
		}

		if err = c.streamFinished(ctx, stream); err != nil {
			errs.AppendErr(err)
		}
	}

	c.streamUpdated(ctx)
	return errs.ToError()
}

func (c *Controller) streamFinished(ctx context.Context, stream *config.Stream) error {
	stream.StreamInfo.Status = livekit.StreamInfo_FINISHED
	stream.UpdateEndTime(time.Now().UnixNano())

	// remove output
	o := c.GetStreamConfig()
	o.Streams.Delete(stream.ParsedUrl)
	c.OutputCount.Dec()

	// end egress if no outputs remaining
	if c.OutputCount.Load() == 0 {
		c.SendEOS(ctx, "all streams removed")
		return nil
	}

	logger.Infow("stream finished",
		"url", stream.RedactedUrl,
		"status", stream.StreamInfo.Status,
		"duration", stream.StreamInfo.Duration,
	)

	return c.streamBin.RemoveStream(stream)
}

func (c *Controller) streamFailed(ctx context.Context, stream *config.Stream, streamErr error) error {
	stream.StreamInfo.Status = livekit.StreamInfo_FAILED
	stream.StreamInfo.Error = streamErr.Error()
	stream.UpdateEndTime(time.Now().UnixNano())

	// remove output
	o := c.GetStreamConfig()
	o.Streams.Delete(stream.ParsedUrl)
	c.OutputCount.Dec()

	// fail egress if no outputs remaining
	if c.OutputCount.Load() == 0 {
		return streamErr
	}

	logger.Infow("stream failed",
		"url", stream.RedactedUrl,
		"status", stream.StreamInfo.Status,
		"duration", stream.StreamInfo.Duration,
		"error", streamErr)

	c.streamUpdated(ctx)
	return c.streamBin.RemoveStream(stream)
}

func (c *Controller) SendEOS(ctx context.Context, reason string) {
	ctx, span := tracer.Start(ctx, "Pipeline.SendEOS")
	defer span.End()

	c.eos.Once(func() {
		if c.limitTimer != nil {
			c.limitTimer.Stop()
		}

		c.Info.Details = fmt.Sprintf("end reason: %s", reason)
		switch c.Info.Status {
		case livekit.EgressStatus_EGRESS_STARTING:
			c.Info.SetAborted(info.MsgStoppedBeforeStarted)
			c.p.Stop()

		case livekit.EgressStatus_EGRESS_ABORTED,
			livekit.EgressStatus_EGRESS_FAILED:
			c.p.Stop()

		case livekit.EgressStatus_EGRESS_ACTIVE:
			c.Info.UpdateStatus(livekit.EgressStatus_EGRESS_ENDING)
			_, _ = c.ipcServiceClient.HandlerUpdate(ctx, (*livekit.EgressInfo)(c.Info))
			c.sendEOS()

		case livekit.EgressStatus_EGRESS_ENDING:
			_, _ = c.ipcServiceClient.HandlerUpdate(ctx, (*livekit.EgressInfo)(c.Info))
			c.sendEOS()

		case livekit.EgressStatus_EGRESS_LIMIT_REACHED:
			c.sendEOS()
		}

		if c.SourceType == types.SourceTypeWeb {
			c.updateDuration(c.src.GetEndedAt())
		}
	})
}

func (c *Controller) sendEOS() {
	go func() {
		logger.Debugw("sending EOS")
		c.eosTimer = time.AfterFunc(time.Second*30, func() {
			c.OnError(errors.ErrPipelineFrozen)
		})
		c.p.SendEOS()
	}()
}

func (c *Controller) OnError(err error) {
	if errors.Is(err, errors.ErrPipelineFrozen) && c.Debug.EnableProfiling {
		c.uploadDebugFiles()
	}

	if c.Info.Status != livekit.EgressStatus_EGRESS_FAILED && (!c.eos.IsBroken() || c.FinalizationRequired) {
		c.Info.SetFailed(err)
	}

	go c.p.Stop()
}

func (c *Controller) Close() {
	if c.SourceType == types.SourceTypeSDK || !c.eos.IsBroken() {
		c.updateDuration(c.src.GetEndedAt())
	}

	// update status
	if c.Info.Status == livekit.EgressStatus_EGRESS_FAILED {
		if o := c.GetStreamConfig(); o != nil {
			o.Streams.Range(func(_, stream any) bool {
				stream.(*config.Stream).StreamInfo.Status = livekit.StreamInfo_FAILED
				return true
			})
		}
	}

	// ensure egress ends with a final state
	switch c.Info.Status {
	case livekit.EgressStatus_EGRESS_STARTING:
		c.Info.SetAborted(info.MsgStoppedBeforeStarted)

	case livekit.EgressStatus_EGRESS_ACTIVE,
		livekit.EgressStatus_EGRESS_ENDING:
		c.Info.SetComplete()
	}

	for _, si := range c.sinks {
		for _, s := range si {
			s.Cleanup()
		}
	}
}

func (c *Controller) startSessionLimitTimer(ctx context.Context) {
	var timeout time.Duration
	for egressType := range c.Outputs {
		var t time.Duration
		switch egressType {
		case types.EgressTypeFile:
			t = c.FileOutputMaxDuration
		case types.EgressTypeStream, types.EgressTypeWebsocket:
			t = c.StreamOutputMaxDuration
		case types.EgressTypeSegments:
			t = c.SegmentOutputMaxDuration
		case types.EgressTypeImages:
			t = c.ImageOutputMaxDuration

		}
		if t > 0 && (timeout == 0 || t < timeout) {
			timeout = t
		}
	}

	if timeout > 0 {
		c.limitTimer = time.AfterFunc(timeout, func() {
			switch c.Info.Status {
			case livekit.EgressStatus_EGRESS_STARTING:
				c.Info.SetAborted(info.MsgLimitReachedWithoutStart)

			case livekit.EgressStatus_EGRESS_ACTIVE:
				c.Info.SetLimitReached()
			}
			if c.playing.IsBroken() {
				c.SendEOS(ctx, "time limit reached")
			} else {
				c.p.Stop()
			}
		})
	}
}

func (c *Controller) updateStartTime(startedAt int64) {
	for egressType, o := range c.Outputs {
		if len(o) == 0 {
			continue
		}
		switch egressType {
		case types.EgressTypeStream, types.EgressTypeWebsocket:
			streamConfig := o[0].(*config.StreamConfig)
			if streamConfig.OutputType == types.OutputTypeRTMP {
				// rtmp has special start time handling
				continue
			}
			streamConfig.Streams.Range(func(_, stream any) bool {
				stream.(*config.Stream).StreamInfo.StartedAt = startedAt
				return true
			})

		case types.EgressTypeFile:
			o[0].(*config.FileConfig).FileInfo.StartedAt = startedAt

		case types.EgressTypeSegments:
			o[0].(*config.SegmentConfig).SegmentsInfo.StartedAt = startedAt

		case types.EgressTypeImages:
			for _, c := range o {
				c.(*config.ImageConfig).ImagesInfo.StartedAt = startedAt
			}
		}
	}

	if c.Info.Status == livekit.EgressStatus_EGRESS_STARTING {
		c.Info.UpdateStatus(livekit.EgressStatus_EGRESS_ACTIVE)
		_, _ = c.ipcServiceClient.HandlerUpdate(context.Background(), (*livekit.EgressInfo)(c.Info))
	}
}

func (c *Controller) updateStreamStartTime(streamID string) {
	if o := c.GetStreamConfig(); o != nil {
		o.Streams.Range(func(_, s any) bool {
			if stream := s.(*config.Stream); stream.StreamID == streamID && stream.StreamInfo.StartedAt == 0 {
				logger.Debugw("stream started", "url", stream.RedactedUrl)
				stream.StreamInfo.StartedAt = time.Now().UnixNano()
				c.Info.UpdatedAt = time.Now().UnixNano()
				return false
			}
			return true
		})
		c.streamUpdated(context.Background())
	}
}

func (c *Controller) streamUpdated(ctx context.Context) {
	c.Info.UpdatedAt = time.Now().UnixNano()

	if o := c.GetStreamConfig(); o != nil {
		skipUpdate := false
		// when adding streams, wait until they've all either started or failed before sending the update
		o.Streams.Range(func(_, stream any) bool {
			streamInfo := stream.(*config.Stream).StreamInfo
			if streamInfo.Status == livekit.StreamInfo_ACTIVE && streamInfo.StartedAt == 0 {
				skipUpdate = true
				return false
			}
			return true
		})
		if skipUpdate {
			return
		}
	}

	_, _ = c.ipcServiceClient.HandlerUpdate(ctx, (*livekit.EgressInfo)(c.Info))
}

func (c *Controller) updateDuration(endedAt int64) {
	for egressType, o := range c.Outputs {
		if len(o) == 0 {
			continue
		}
		switch egressType {
		case types.EgressTypeStream, types.EgressTypeWebsocket:
			streamConfig := o[0].(*config.StreamConfig)
			streamConfig.Streams.Range(func(_, s any) bool {
				stream := s.(*config.Stream)
				stream.StreamInfo.Status = livekit.StreamInfo_FINISHED
				stream.UpdateEndTime(endedAt)
				return true
			})

		case types.EgressTypeFile:
			fileInfo := o[0].(*config.FileConfig).FileInfo
			if fileInfo.StartedAt == 0 {
				fileInfo.StartedAt = endedAt
			}
			fileInfo.EndedAt = endedAt
			fileInfo.Duration = endedAt - fileInfo.StartedAt

		case types.EgressTypeSegments:
			segmentsInfo := o[0].(*config.SegmentConfig).SegmentsInfo
			if segmentsInfo.StartedAt == 0 {
				segmentsInfo.StartedAt = endedAt
			}
			segmentsInfo.EndedAt = endedAt
			segmentsInfo.Duration = endedAt - segmentsInfo.StartedAt

		case types.EgressTypeImages:
			for _, c := range o {
				imageInfo := c.(*config.ImageConfig).ImagesInfo
				if imageInfo.StartedAt == 0 {
					imageInfo.StartedAt = endedAt
				}
				imageInfo.EndedAt = endedAt
			}
		}
	}
}
