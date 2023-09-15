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
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/tinyzimmer/go-gst/gst"
	"go.uber.org/zap"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/pipeline/sink"
	"github.com/livekit/egress/pkg/pipeline/source"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/protocol/utils"
)

const (
	pipelineName = "pipeline"
)

type Controller struct {
	*config.PipelineConfig

	// gstreamer
	src       source.Source
	p         *gstreamer.Pipeline
	sinks     map[types.EgressType]sink.Sink
	streamBin *builder.StreamBin
	callbacks *gstreamer.Callbacks

	// internal
	mu         sync.Mutex
	gstLogger  *zap.SugaredLogger
	limitTimer *time.Timer
	playing    core.Fuse
	eos        core.Fuse
	stopped    core.Fuse
}

func New(ctx context.Context, conf *config.PipelineConfig) (*Controller, error) {
	ctx, span := tracer.Start(ctx, "Pipeline.New")
	defer span.End()

	var err error
	c := &Controller{
		PipelineConfig: conf,
		callbacks: &gstreamer.Callbacks{
			GstReady: make(chan struct{}),
		},
		gstLogger: logger.GetLogger().(*logger.ZapLogger).ToZap().WithOptions(zap.WithCaller(false)),
		playing:   core.NewFuse(),
		eos:       core.NewFuse(),
		stopped:   core.NewFuse(),
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
	c.sinks, err = sink.CreateSinks(conf, c.callbacks)
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
	p, err := gstreamer.NewPipeline(pipelineName, c.Latency, c.callbacks)
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

	for egressType := range c.Outputs {
		var sinkBin *gstreamer.Bin
		switch egressType {
		case types.EgressTypeFile:
			sinkBin, err = builder.BuildFileBin(p, c.PipelineConfig)

		case types.EgressTypeSegments:
			sinkBin, err = builder.BuildSegmentBin(p, c.PipelineConfig)

		case types.EgressTypeStream:
			c.streamBin, sinkBin, err = builder.BuildStreamBin(p, c.PipelineConfig)

		case types.EgressTypeWebsocket:
			writer := c.sinks[egressType].(*sink.WebsocketSink)
			sinkBin, err = builder.BuildWebsocketBin(p, writer.SinkCallbacks())
		}
		if err != nil {
			return err
		}
		if err = p.AddSinkBin(sinkBin); err != nil {
			return err
		}
	}

	if err = p.Link(); err != nil {
		return err
	}

	c.p = p
	return nil
}

func (c *Controller) Run(ctx context.Context) *livekit.EgressInfo {
	ctx, span := tracer.Start(ctx, "Pipeline.Run")
	defer span.End()

	c.Info.StartedAt = time.Now().UnixNano()
	defer c.Close()

	// session limit timer
	c.startSessionLimitTimer(ctx)

	// close when room ends
	go func() {
		<-c.src.EndRecording()
		c.SendEOS(ctx)
	}()

	// wait until room is ready
	start := c.src.StartRecording()
	if start != nil {
		logger.Debugw("waiting for start signal")
		select {
		case <-c.stopped.Watch():
			c.Info.Status = livekit.EgressStatus_EGRESS_ABORTED
			return c.Info
		case <-start:
			// continue
		}
	}

	for _, s := range c.sinks {
		if err := s.Start(); err != nil {
			c.Info.Error = err.Error()
			return c.Info
		}
	}

	if err := c.p.Run(); err != nil {
		c.Info.Error = err.Error()
		return c.Info
	}

	for _, s := range c.sinks {
		if err := s.Close(); err != nil {
			c.Info.Error = err.Error()
			return c.Info
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

	sendUpdate := false
	errs := errors.ErrArray{}
	now := time.Now().UnixNano()

	// add stream outputs first
	for _, rawUrl := range req.AddOutputUrls {
		// validate and redact url
		url, redacted, err := c.ValidateUrl(rawUrl, types.OutputTypeRTMP)
		if err != nil {
			errs.AppendErr(err)
			continue
		}

		// add stream
		if err = c.streamBin.AddStream(url); err != nil {
			errs.AppendErr(err)
			continue
		}

		// add to output count
		c.OutputCount++

		// add stream info to results
		c.mu.Lock()
		streamInfo := &livekit.StreamInfo{
			Url:       redacted,
			StartedAt: now,
			Status:    livekit.StreamInfo_ACTIVE,
		}
		o.StreamInfo[url] = streamInfo
		c.Info.StreamResults = append(c.Info.StreamResults, streamInfo)
		if list := c.Info.GetStream(); list != nil {
			list.Info = append(list.Info, streamInfo)
		}
		c.mu.Unlock()
		sendUpdate = true
	}

	// remove stream outputs
	for _, rawUrl := range req.RemoveOutputUrls {
		url, _, err := c.ValidateUrl(rawUrl, types.OutputTypeRTMP)
		if err != nil {
			errs.AppendErr(err)
			continue
		}

		if err = c.removeSink(ctx, url, nil); err != nil {
			errs.AppendErr(err)
		} else {
			sendUpdate = true
		}
	}

	if sendUpdate {
		c.Info.UpdatedAt = time.Now().UnixNano()
		c.OnUpdate(ctx, c.Info)
	}

	return errs.ToError()
}

func (c *Controller) removeSink(ctx context.Context, url string, streamErr error) error {
	now := time.Now().UnixNano()

	c.mu.Lock()
	o := c.GetStreamConfig()

	streamInfo := o.StreamInfo[url]
	if streamInfo == nil {
		c.mu.Unlock()
		return errors.ErrStreamNotFound(url)
	}

	// set error if exists
	if streamErr != nil {
		streamInfo.Status = livekit.StreamInfo_FAILED
		streamInfo.Error = streamErr.Error()
	} else {
		streamInfo.Status = livekit.StreamInfo_FINISHED
	}

	// update end time and duration
	streamInfo.EndedAt = now
	if streamInfo.StartedAt == 0 {
		streamInfo.StartedAt = now
	} else {
		streamInfo.Duration = now - streamInfo.StartedAt
	}

	// remove output
	delete(o.StreamInfo, url)
	c.OutputCount--
	c.mu.Unlock()

	// log removal
	redacted, _ := utils.RedactStreamKey(url)
	logger.Infow("removing stream sink",
		"url", redacted,
		"status", streamInfo.Status,
		"duration", streamInfo.Duration,
		"error", streamErr)

	// shut down if no outputs remaining
	if c.OutputCount == 0 {
		if streamErr != nil {
			return streamErr
		} else {
			c.SendEOS(ctx)
			return nil
		}
	}

	// only send updates if the egress will continue, otherwise it's handled by UpdateStream RPC
	if streamErr != nil {
		c.Info.UpdatedAt = time.Now().UnixNano()
		c.OnUpdate(ctx, c.Info)
	}

	return c.streamBin.RemoveStream(url)
}

func (c *Controller) SendEOS(ctx context.Context) {
	ctx, span := tracer.Start(ctx, "Pipeline.SendEOS")
	defer span.End()

	c.eos.Once(func() {
		logger.Debugw("Sending EOS")

		if c.limitTimer != nil {
			c.limitTimer.Stop()
		}
		switch c.Info.Status {
		case livekit.EgressStatus_EGRESS_STARTING:
			c.Info.Status = livekit.EgressStatus_EGRESS_ABORTED
			fallthrough

		case livekit.EgressStatus_EGRESS_ABORTED,
			livekit.EgressStatus_EGRESS_FAILED:
			c.p.Stop()

		case livekit.EgressStatus_EGRESS_ACTIVE:
			c.Info.UpdatedAt = time.Now().UnixNano()
			if c.Info.Error != "" {
				c.Info.Status = livekit.EgressStatus_EGRESS_FAILED
				c.p.Stop()
			} else {
				c.Info.Status = livekit.EgressStatus_EGRESS_ENDING
				c.OnUpdate(ctx, c.Info)
			}
			fallthrough

		case livekit.EgressStatus_EGRESS_ENDING,
			livekit.EgressStatus_EGRESS_LIMIT_REACHED:
			go c.p.SendEOS()
		}

		if c.SourceType == types.SourceTypeWeb {
			c.updateDuration(c.src.GetEndedAt())
		}
	})
}

func (c *Controller) OnError(err error) {
	if errors.Is(err, errors.ErrPipelineFrozen) && c.Debug.EnableProfiling {
		c.uploadDebugFiles()
	}

	if c.Info.Error == "" && (!c.eos.IsBroken() || c.FinalizationRequired) {
		c.Info.Error = err.Error()
	}

	go c.p.Stop()
}

func (c *Controller) Close() {
	if c.SourceType == types.SourceTypeSDK || !c.eos.IsBroken() {
		c.updateDuration(c.src.GetEndedAt())
	}
	c.src.Close()

	now := time.Now().UnixNano()
	c.Info.UpdatedAt = now
	c.Info.EndedAt = now

	// update status
	if c.Info.Error != "" {
		c.Info.Status = livekit.EgressStatus_EGRESS_FAILED
		if o := c.GetStreamConfig(); o != nil {
			for _, streamInfo := range o.StreamInfo {
				streamInfo.Status = livekit.StreamInfo_FAILED
			}
		}
	}

	// ensure egress ends with a final state
	switch c.Info.Status {
	case livekit.EgressStatus_EGRESS_STARTING:
		c.Info.Status = livekit.EgressStatus_EGRESS_ABORTED

	case livekit.EgressStatus_EGRESS_ACTIVE,
		livekit.EgressStatus_EGRESS_ENDING:
		c.Info.Status = livekit.EgressStatus_EGRESS_COMPLETE
	}

	for _, s := range c.sinks {
		s.Cleanup()
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
		}
		if t > 0 && (timeout == 0 || t < timeout) {
			timeout = t
		}
	}

	if timeout > 0 {
		c.limitTimer = time.AfterFunc(timeout, func() {
			switch c.Info.Status {
			case livekit.EgressStatus_EGRESS_STARTING,
				livekit.EgressStatus_EGRESS_ACTIVE:
				c.Info.Status = livekit.EgressStatus_EGRESS_LIMIT_REACHED
			}
			if c.playing.IsBroken() {
				c.SendEOS(ctx)
			} else {
				c.p.Stop()
			}
		})
	}
}

func (c *Controller) updateStartTime(startedAt int64) {
	for egressType, o := range c.Outputs {
		switch egressType {
		case types.EgressTypeStream, types.EgressTypeWebsocket:
			c.mu.Lock()
			for _, streamInfo := range o.(*config.StreamConfig).StreamInfo {
				streamInfo.Status = livekit.StreamInfo_ACTIVE
				streamInfo.StartedAt = startedAt
			}
			c.mu.Unlock()

		case types.EgressTypeFile:
			o.(*config.FileConfig).FileInfo.StartedAt = startedAt

		case types.EgressTypeSegments:
			o.(*config.SegmentConfig).SegmentsInfo.StartedAt = startedAt
		}
	}

	if c.Info.Status == livekit.EgressStatus_EGRESS_STARTING {
		c.Info.Status = livekit.EgressStatus_EGRESS_ACTIVE
		c.Info.UpdatedAt = time.Now().UnixNano()
		c.OnUpdate(context.Background(), c.Info)
	}
}

func (c *Controller) updateDuration(endedAt int64) {
	for egressType, o := range c.Outputs {
		switch egressType {
		case types.EgressTypeStream, types.EgressTypeWebsocket:
			for _, info := range o.(*config.StreamConfig).StreamInfo {
				info.Status = livekit.StreamInfo_FINISHED
				if info.StartedAt == 0 {
					info.StartedAt = endedAt
				}
				info.EndedAt = endedAt
				info.Duration = endedAt - info.StartedAt
			}

		case types.EgressTypeFile:
			fileInfo := o.(*config.FileConfig).FileInfo
			if fileInfo.StartedAt == 0 {
				fileInfo.StartedAt = endedAt
			}
			fileInfo.EndedAt = endedAt
			fileInfo.Duration = endedAt - fileInfo.StartedAt

		case types.EgressTypeSegments:
			segmentsInfo := o.(*config.SegmentConfig).SegmentsInfo
			if segmentsInfo.StartedAt == 0 {
				segmentsInfo.StartedAt = endedAt
			}
			segmentsInfo.EndedAt = endedAt
			segmentsInfo.Duration = endedAt - segmentsInfo.StartedAt
		}
	}
}
