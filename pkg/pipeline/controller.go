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
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/linkdata/deadlock"
	"go.uber.org/atomic"
	"go.uber.org/zap"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/pipeline/sink"
	"github.com/livekit/egress/pkg/pipeline/source"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
	"go.opentelemetry.io/otel"
)

const (
	pipelineName = "pipeline"
	eosTimeout   = time.Second * 30

	streamRetryUpdateInterval = time.Minute
)

type Controller struct {
	*config.PipelineConfig
	ipcServiceClient ipc.EgressServiceClient

	// gstreamer
	gstLogger *zap.SugaredLogger
	src       source.Source
	callbacks *gstreamer.Callbacks
	p         *gstreamer.Pipeline
	sinks     map[types.EgressType][]sink.Sink

	// internal
	mu                   deadlock.Mutex
	monitor              *stats.HandlerMonitor
	limitTimer           *time.Timer
	storageMonitorCancel context.CancelFunc
	paused               core.Fuse
	playing              core.Fuse
	eosSent              core.Fuse
	eosTimer             *time.Timer
	eosReceived          core.Fuse
	stopped              core.Fuse
	storageLimitOnce     sync.Once
	stats                controllerStats
	pipelineCreatedAt    time.Time
}

type controllerStats struct {
	droppedAudioBuffers      atomic.Uint64
	droppedAudioDuration     atomic.Duration
	droppedVideoBuffers      atomic.Uint64
	droppedVideoBuffersByQueue map[string]uint64
}

var (
	tracer = otel.Tracer("github.com/livekit/egress/pkg/pipeline")
)

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
		sinks:   make(map[types.EgressType][]sink.Sink),
		monitor: stats.NewHandlerMonitor(conf.NodeID, conf.ClusterID, conf.Info.EgressId),
		stats: controllerStats{
			droppedVideoBuffersByQueue: make(map[string]uint64),
		},
	}
	c.callbacks.SetOnError(c.OnError)
	c.callbacks.SetOnEOSSent(c.onEOSSent)
	c.callbacks.SetOnDebugDotRequest(func(reason string) {
		if !c.Debug.EnableProfiling {
			return
		}
		logger.Debugw("debug dot requested", "reason", reason)
		c.generateDotFile(reason)
	})

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

	// create pipeline
	<-c.callbacks.GstReady
	if err = c.BuildPipeline(); err != nil {
		c.src.Close()
		return nil, err
	}

	return c, nil
}

func (c *Controller) BuildPipeline() error {
	p, err := gstreamer.NewPipeline(pipelineName, c.Latency.PipelineLatency, c.callbacks)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	c.pipelineCreatedAt = time.Now()

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

	for egressType, outputs := range c.Outputs {
		for _, o := range outputs {
			s, err := sink.NewSink(p, c.PipelineConfig, egressType, o, c.callbacks, c.monitor)
			if err != nil {
				return err
			}
			c.sinks[egressType] = append(c.sinks[egressType], s)
		}
	}

	if err = p.Link(); err != nil {
		return err
	}

	// initial graph is fully wired; from now on, dynamic additions must be linked immediately
	p.UpgradeState(gstreamer.StateStarted)

	c.p = p
	if timeAware, ok := c.src.(source.TimeAware); ok {
		timeAware.SetTimeProvider(p)
	}
	close(c.callbacks.BuildReady)
	return nil
}

func (c *Controller) Run(ctx context.Context) *livekit.EgressInfo {
	ctx, span := tracer.Start(ctx, "Pipeline.Run")
	defer span.End()

	defer c.Close()

	defer func() {
		if c.VideoEnabled {
			logger.Infow(
				"video input queue stats",
				"videoBuffersDropped", c.stats.droppedVideoBuffers.Load(),
				"requestType", c.RequestType,
				"sourceType", c.SourceType,
				"droppedByQueue", c.stats.droppedVideoBuffersByQueue,
			)
		}
		if c.SourceType == types.SourceTypeSDK {
			logger.Debugw(
				"audio qos stats",
				"audioBuffersDropped", c.stats.droppedAudioBuffers.Load(),
				"totalAudioDurationDropped", c.stats.droppedAudioDuration.Load(),
				"requestType", c.RequestType,
			)
		}
	}()

	// session limit timer
	c.startSessionLimitTimer(ctx)

	// close when room ends
	go func() {
		<-c.src.EndRecording()
		c.SendEOS(ctx, livekit.EndReasonSrcClosed)
	}()

	// wait until room is ready
	start := c.src.StartRecording()
	if start != nil {
		logger.Debugw("waiting for start signal")
		select {
		case <-c.stopped.Watch():
			c.src.Close()
			c.Info.SetAborted(livekit.MsgStartNotReceived)
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

	c.startOutputSizeMonitor()

	err := c.p.Run()
	if err != nil {
		c.src.Close()
		c.Info.SetFailed(err)
		return c.Info
	}

	logger.Debugw("closing source")
	c.src.Close()

	if c.playing.IsBroken() {
		logger.Debugw("closing sinks")
		for _, si := range c.sinks {
			for _, s := range si {
				if c.eosReceived.IsBroken() || s.EOSReceived() {
					if err := s.Close(); err != nil && c.Info.Status != livekit.EgressStatus_EGRESS_FAILED {
						c.Info.SetFailed(err)
					}
				}
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
		if list := c.Info.GetStream(); list != nil { //nolint:staticcheck // keep deprecated field for older clients
			list.Info = append(list.Info, stream.StreamInfo)
		}
		c.mu.Unlock()

		// add stream
		if err = c.getStreamSink().AddStream(stream); err != nil {
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
		c.SendEOS(ctx, livekit.EndReasonStreamsStopped)
		return nil
	}

	logger.Infow("stream finished",
		"url", stream.RedactedUrl,
		"status", stream.StreamInfo.Status,
		"duration", stream.StreamInfo.Duration,
	)

	return c.getStreamSink().RemoveStream(stream)
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
		return psrpc.NewError(psrpc.Unavailable, streamErr)
	}

	logger.Infow("stream failed",
		"url", stream.RedactedUrl,
		"status", stream.StreamInfo.Status,
		"duration", stream.StreamInfo.Duration,
		"error", streamErr)

	c.streamUpdated(ctx)
	return c.getStreamSink().RemoveStream(stream)
}

func (c *Controller) trackStreamRetry(ctx context.Context, stream *config.Stream) {
	now := time.Now()
	stream.StreamInfo.LastRetryAt = now.UnixNano()
	stream.StreamInfo.Retries++
	if !stream.ShouldSendRetryUpdate(now, streamRetryUpdateInterval) {
		return
	}
	logger.Infow("retrying stream update",
		"url", stream.RedactedUrl,
		"retries", stream.StreamInfo.Retries,
	)

	c.streamUpdated(ctx)
}

func (c *Controller) onEOSSent() {
	// for video-only track/track composite, EOS might have already
	// made it through the pipeline by the time endRecording is closed
	if (c.RequestType == types.RequestTypeTrack || c.RequestType == types.RequestTypeTrackComposite) && !c.AudioEnabled {
		// this will not actually send a second EOS, but will make sure everything is in the correct state
		c.SendEOS(context.Background(), livekit.EndReasonSrcClosed)
	}
}

func (c *Controller) onStorageLimitReached() {
	c.storageLimitOnce.Do(func() {
		c.Info.SetLimitReached()
		c.SendEOS(context.Background(), livekit.EndReasonLimitReached)
	})
}

func (c *Controller) SendEOS(ctx context.Context, reason string) {
	ctx, span := tracer.Start(ctx, "Pipeline.SendEOS")
	defer span.End()

	c.eosSent.Once(func() {
		if c.limitTimer != nil {
			c.limitTimer.Stop()
		}

		c.Info.SetEndReason(reason)
		logger.Debugw("stopping pipeline", "reason", reason)

		switch c.Info.Status {
		case livekit.EgressStatus_EGRESS_STARTING:
			c.Info.SetAborted(livekit.MsgStoppedBeforeStarted)
			c.p.Stop()

		case livekit.EgressStatus_EGRESS_ABORTED,
			livekit.EgressStatus_EGRESS_FAILED:
			c.p.Stop()

		case livekit.EgressStatus_EGRESS_ACTIVE:
			c.Info.UpdateStatus(livekit.EgressStatus_EGRESS_ENDING)
			_, _ = c.ipcServiceClient.HandlerUpdate(ctx, c.Info)
			c.sendEOS()

		case livekit.EgressStatus_EGRESS_ENDING:
			_, _ = c.ipcServiceClient.HandlerUpdate(ctx, c.Info)
			c.sendEOS()

		case livekit.EgressStatus_EGRESS_LIMIT_REACHED:
			c.sendEOS()
		}

		if c.SourceType == types.SourceTypeWeb {
			// web source uses the current time
			c.updateEndTime()
		}
	})
}

func (c *Controller) sendEOS() {
	for _, sinks := range c.sinks {
		for _, s := range sinks {
			s.AddEOSProbe()
		}
	}

	c.eosTimer = time.AfterFunc(eosTimeout, func() {
		logger.Debugw("eos timer firing")
		for egressType, si := range c.sinks {
			switch egressType {
			case types.EgressTypeFile, types.EgressTypeSegments, types.EgressTypeImages:
				for _, s := range si {
					if !s.EOSReceived() {
						c.OnError(errors.ErrPipelineFrozen)
						return
					}
				}
			default:
				// finalization not required
			}
		}
		c.p.Stop()
	})

	go func() {
		c.p.SendEOS()
		logger.Debugw("eos sent")
	}()
}

func (c *Controller) OnError(err error) {
	logger.Errorw("controller onError invoked", err)
	if errors.Is(err, errors.ErrPipelineFrozen) && c.Debug.EnableProfiling {
		c.generateDotFile("error")
		c.generatePProf()
	}

	if c.Info.Status != livekit.EgressStatus_EGRESS_FAILED && (!c.eosSent.IsBroken() || c.FinalizationRequired) {
		c.Info.SetFailed(err)
	}

	go c.p.Stop()
}

func (c *Controller) Close() {
	c.stopOutputSizeMonitor()

	if c.SourceType == types.SourceTypeSDK || !c.eosSent.IsBroken() {
		// sdk source will use the timestamp of the last packet pushed to the pipeline
		c.updateEndTime()
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
		c.Info.SetAborted(livekit.MsgStoppedBeforeStarted)

	case livekit.EgressStatus_EGRESS_ACTIVE,
		livekit.EgressStatus_EGRESS_ENDING:
		c.Info.SetComplete()
		fallthrough

	case livekit.EgressStatus_EGRESS_LIMIT_REACHED,
		livekit.EgressStatus_EGRESS_COMPLETE:
		// upload manifest and add location to egress info
		c.uploadManifest()
	}

	// upload debug files
	c.uploadDebugFiles()
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
				c.Info.SetAborted(livekit.MsgLimitReachedWithoutStart)
			case livekit.EgressStatus_EGRESS_ACTIVE:
				c.Info.SetLimitReached()
			}

			if c.playing.IsBroken() {
				c.SendEOS(ctx, livekit.EndReasonLimitReached)
			} else {
				c.p.Stop()
			}
		})
	}
}

func (c *Controller) startOutputSizeMonitor() {
	ctx, cancel := context.WithCancel(context.Background())
	c.storageMonitorCancel = cancel

	c.p.AddOnStop(func() error {
		cancel()
		return nil
	})

	go c.monitorOutputDirSize(ctx)
}

func (c *Controller) stopOutputSizeMonitor() {
	if c.storageMonitorCancel != nil {
		c.storageMonitorCancel()
		c.storageMonitorCancel = nil
	}
}

func (c *Controller) monitorOutputDirSize(ctx context.Context) {
	thresholds := []int64{
		1 << 30,  // 1GB
		3 << 30,  // 3GB
		5 << 30,  // 5GB
		10 << 30, // 10GB
		20 << 30, // 20GB
		50 << 30, // 50GB
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	nextThreshold := 0
	statErrorLogged := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		size, files, err := c.getOutputDirStats()
		if err != nil {
			if !statErrorLogged {
				logger.Debugw("failed to stat output directory", err, "dir", c.TmpDir)
				statErrorLogged = true
			}
			continue
		}
		statErrorLogged = false

		if c.FileOutputMaxSize > 0 && size >= c.FileOutputMaxSize {
			c.logOutputFileSizes(files, 10)
			logger.Warnw(
				"output storage limit reached",
				nil,
				"dir", c.TmpDir,
				"bytesWritten", size,
				"limitBytes", c.FileOutputMaxSize,
			)
			c.onStorageLimitReached()
			return
		}

		thresholdTriggered := false
		for nextThreshold < len(thresholds) && size >= thresholds[nextThreshold] {
			logger.Debugw(
				"output size threshold exceeded",
				"dir", c.TmpDir,
				"bytesWritten", size,
				"thresholdBytes", thresholds[nextThreshold],
			)
			thresholdTriggered = true
			nextThreshold++
		}
		if thresholdTriggered {
			c.logOutputFileSizes(files, 10)
		}
	}
}

type outputFileStat struct {
	path string
	size int64
}

func (c *Controller) getOutputDirStats() (int64, []outputFileStat, error) {
	if c.TmpDir == "" {
		return 0, nil, nil
	}

	var files []outputFileStat

	var total int64

	err := filepath.Walk(c.TmpDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}

		if info.IsDir() {
			return nil
		}

		total += info.Size()

		rel, relErr := filepath.Rel(c.TmpDir, p)
		if relErr != nil {
			rel = p
		}

		files = append(files, outputFileStat{
			path: rel,
			size: info.Size(),
		})

		return nil
	})
	if err != nil {
		return 0, nil, err
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].size > files[j].size
	})

	return total, files, nil
}

func (c *Controller) logOutputFileSizes(files []outputFileStat, limit int) {
	if files == nil {
		return
	}

	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}

	for _, f := range files {
		logger.Infow("output file size", "file", f.path, "bytes", f.size)
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
		_, _ = c.ipcServiceClient.HandlerUpdate(context.Background(), c.Info)
	}
}

func (c *Controller) updateStreamStartTime(streamID string) {
	if o := c.GetStreamConfig(); o != nil {
		o.Streams.Range(func(_, s any) bool {
			if stream := s.(*config.Stream); stream.StreamID == streamID && stream.StreamInfo.StartedAt == 0 {
				logger.Debugw("stream started", "url", stream.RedactedUrl)
				stream.StreamInfo.StartedAt = time.Now().UnixNano()
				c.Info.UpdatedAt = time.Now().UnixNano()
				c.streamUpdated(context.Background())
				return false
			}
			return true
		})
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

	_, _ = c.ipcServiceClient.HandlerUpdate(ctx, c.Info)
}

func (c *Controller) updateEndTime() {
	endedAt := c.src.GetEndedAt()

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

// uploadManifest happens last, after all sinks have finished
func (c *Controller) uploadManifest() {
	if c.Manifest == nil {
		return
	}

	b, err := c.Manifest.Close(c.Info.EndedAt)
	if err != nil {
		logger.Errorw("failed to close manifest", err)
		return
	}

	manifestPath := path.Join(c.TmpDir, fmt.Sprintf("%s.json", c.Info.EgressId))
	f, err := os.Create(manifestPath)
	if err != nil {
		logger.Errorw("failed to create manifest file", err)
		return
	}

	_, err = f.Write(b)
	if err != nil {
		logger.Errorw("failed to write to manifest file", err)
		return
	}
	_ = f.Close()

	infoUpdated := false
	for _, si := range c.sinks {
		for _, s := range si {
			location, uploaded, err := s.UploadManifest(manifestPath)
			if err != nil {
				logger.Errorw("failed to upload manifest", err)
				continue
			}

			if !infoUpdated && uploaded {
				c.Info.ManifestLocation = location
				infoUpdated = true
			}
		}
	}
}

func (c *Controller) getStreamSink() *sink.StreamSink {
	s := c.sinks[types.EgressTypeStream]
	if len(s) == 0 {
		return nil
	}

	return s[0].(*sink.StreamSink)
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
		if i := si.(*sink.ImageSink); i.Id == id {
			return i
		}
	}

	return nil
}
