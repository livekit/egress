package pipeline

import "C"
import (
	"context"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"
	"go.uber.org/zap"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/input"
	"github.com/livekit/egress/pkg/pipeline/output"
	"github.com/livekit/egress/pkg/pipeline/sink"
	"github.com/livekit/egress/pkg/pipeline/source"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/protocol/utils"
)

const (
	pipelineSource = "pipeline"
	eosTimeout     = time.Second * 30
)

type UpdateFunc func(context.Context, *livekit.EgressInfo)

type Pipeline struct {
	*config.PipelineConfig

	// gstreamer
	src      source.Source
	loop     *glib.MainLoop
	pipeline *gst.Pipeline
	in       *input.Bin
	out      *output.Bin
	sinks    map[types.EgressType]sink.Sink

	// internal
	mu         sync.Mutex
	gstLogger  *zap.SugaredLogger
	playing    bool
	limitTimer *time.Timer
	closed     core.Fuse
	eosTimer   *time.Timer

	// callbacks
	sendUpdate UpdateFunc
}

func New(ctx context.Context, conf *config.PipelineConfig, onStatusUpdate UpdateFunc) (*Pipeline, error) {
	ctx, span := tracer.Start(ctx, "Pipeline.New")
	defer span.End()

	var err error
	p := &Pipeline{
		PipelineConfig: conf,
		gstLogger:      logger.GetLogger().(*logger.ZapLogger).ToZap().WithOptions(zap.WithCaller(false)),
		closed:         core.NewFuse(),
		sendUpdate:     onStatusUpdate,
	}

	// initialize gst
	go func() {
		_, span := tracer.Start(ctx, "gst.Init")
		defer span.End()
		gst.Init(nil)
		gst.SetLogFunction(p.gstLog)
		close(conf.GstReady)
	}()

	// create source
	p.src, err = source.New(ctx, conf)
	if err != nil {
		return nil, err
	}

	// create pipeline
	<-conf.GstReady
	p.pipeline, err = gst.NewPipeline("pipeline")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	// create input bin
	p.in, err = input.New(ctx, p.pipeline, conf)
	if err != nil {
		return nil, err
	}

	// create output bin
	p.out, err = output.New(ctx, p.pipeline, conf)
	if err != nil {
		return nil, err
	}

	// link input bin
	audioSrcPad, videoSrcPad, err := p.in.Link()
	if err != nil {
		return nil, err
	}

	// link output bin
	if err = p.out.Link(audioSrcPad, videoSrcPad); err != nil {
		return nil, err
	}

	// create sinks
	p.sinks, err = sink.CreateSinks(conf)
	if err != nil {
		return nil, err
	}

	if s, ok := p.sinks[types.EgressTypeWebsocket]; ok {
		websocketSink := s.(*sink.WebsocketSink)
		p.src.(*source.SDKSource).OnTrackMuted(websocketSink.OnTrackMuted)
		if err = p.out.SetWebsocketSink(websocketSink); err != nil {
			return nil, err
		}
	}

	return p, nil
}

func (p *Pipeline) Run(ctx context.Context) *livekit.EgressInfo {
	ctx, span := tracer.Start(ctx, "Pipeline.Run")
	defer span.End()

	p.Info.StartedAt = time.Now().UnixNano()
	defer func() {
		now := time.Now().UnixNano()
		p.Info.UpdatedAt = now
		p.Info.EndedAt = now

		// update status
		if p.Info.Error != "" {
			p.Info.Status = livekit.EgressStatus_EGRESS_FAILED

			if o := p.GetStreamConfig(); o != nil {
				for _, streamInfo := range o.StreamInfo {
					streamInfo.Status = livekit.StreamInfo_FAILED
				}
			}
		}

		// ensure egress ends with a final state
		switch p.Info.Status {
		case livekit.EgressStatus_EGRESS_STARTING:
			p.Info.Status = livekit.EgressStatus_EGRESS_ABORTED

		case livekit.EgressStatus_EGRESS_ACTIVE,
			livekit.EgressStatus_EGRESS_ENDING:
			p.Info.Status = livekit.EgressStatus_EGRESS_COMPLETE
		}

		for _, s := range p.sinks {
			s.Cleanup()
		}
	}()

	// session limit timer
	p.startSessionLimitTimer(ctx)

	// close when room ends
	go func() {
		<-p.src.EndRecording()
		p.SendEOS(ctx)
	}()

	// wait until room is ready
	start := p.src.StartRecording()
	if start != nil {
		logger.Debugw("waiting for start signal")
		select {
		case <-p.closed.Watch():
			p.src.Close()
			p.Info.Status = livekit.EgressStatus_EGRESS_ABORTED
			return p.Info
		case <-start:
			// continue
		}
	}

	for _, s := range p.sinks {
		if err := s.Start(); err != nil {
			p.src.Close()
			p.Info.Error = err.Error()
			return p.Info
		}
	}

	// add watch
	p.loop = glib.NewMainLoop(glib.MainContextDefault(), false)
	p.pipeline.GetPipelineBus().AddWatch(p.messageWatch)

	// set state to playing (this does not start the pipeline)
	if err := p.pipeline.SetState(gst.StatePlaying); err != nil {
		span.RecordError(err)
		logger.Errorw("failed to set pipeline state", err)
		p.Info.Error = err.Error()
		return p.Info
	}

	// stop if one of the sources or sinks fails
	go func() {
		err := <-p.Failure
		if p.Info.Error == "" {
			p.Info.Error = err.Error()
		}
		p.stop()
	}()

	// run main loop
	p.loop.Run()

	// close input source
	p.src.Close()

	// update endedAt from sdk source
	if p.SourceType == types.SourceTypeSDK {
		p.updateDuration(p.src.(*source.SDKSource).GetEndTime())
	}

	// return if error or aborted
	if p.Info.Error != "" || p.Info.Status == livekit.EgressStatus_EGRESS_ABORTED {
		return p.Info
	}

	// finalize
	errs := errors.ErrArray{}
	for _, s := range p.sinks {
		if err := s.Finalize(); err != nil {
			errs.AppendErr(err)
		}
	}
	if err := errs.ToError(); err != nil {
		p.Info.Error = err.Error()
	}

	return p.Info
}

func (p *Pipeline) UpdateStream(ctx context.Context, req *livekit.UpdateStreamRequest) error {
	ctx, span := tracer.Start(ctx, "Pipeline.UpdateStream")
	defer span.End()

	o := p.GetStreamConfig()
	if o == nil {
		return errors.ErrNonStreamingPipeline
	}

	sendUpdate := false
	errs := errors.ErrArray{}
	now := time.Now().UnixNano()

	// add stream outputs first
	for _, rawUrl := range req.AddOutputUrls {
		// validate and redact url
		url, redacted, err := p.ValidateUrl(rawUrl, types.OutputTypeRTMP)
		if err != nil {
			errs.AppendErr(err)
			continue
		}

		// add stream
		if err := p.out.AddStream(url); err != nil {
			errs.AppendErr(err)
			continue
		}

		// add to output count
		p.OutputCount++

		// add stream info to results
		p.mu.Lock()
		streamInfo := &livekit.StreamInfo{
			Url:       redacted,
			StartedAt: now,
			Status:    livekit.StreamInfo_ACTIVE,
		}
		o.StreamInfo[url] = streamInfo
		p.Info.StreamResults = append(p.Info.StreamResults, streamInfo)
		if list := p.Info.GetStream(); list != nil {
			list.Info = append(list.Info, streamInfo)
		}
		p.mu.Unlock()
		sendUpdate = true
	}

	// remove stream outputs
	for _, rawUrl := range req.RemoveOutputUrls {
		url, _, err := p.ValidateUrl(rawUrl, types.OutputTypeRTMP)
		if err != nil {
			errs.AppendErr(err)
			continue
		}

		if err = p.removeSink(ctx, url, nil); err != nil {
			errs.AppendErr(err)
		} else {
			sendUpdate = true
		}
	}

	if sendUpdate {
		p.Info.UpdatedAt = time.Now().UnixNano()
		p.sendUpdate(ctx, p.Info)
	}

	return errs.ToError()
}

func (p *Pipeline) removeSink(ctx context.Context, url string, streamErr error) error {
	now := time.Now().UnixNano()

	p.mu.Lock()
	o := p.GetStreamConfig()

	streamInfo := o.StreamInfo[url]
	if streamInfo == nil {
		p.mu.Unlock()
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
	p.OutputCount--
	p.mu.Unlock()

	// log removal
	redacted, _ := utils.RedactStreamKey(url)
	logger.Infow("removing stream sink",
		"url", redacted,
		"status", streamInfo.Status,
		"duration", streamInfo.Duration,
		"error", streamErr)

	// shut down if no outputs remaining
	if p.OutputCount == 0 {
		if streamErr != nil {
			return streamErr
		} else {
			p.SendEOS(ctx)
			return nil
		}
	}

	// only send updates if the egress will continue, otherwise it's handled by UpdateStream RPC
	if streamErr != nil {
		p.Info.UpdatedAt = time.Now().UnixNano()
		p.sendUpdate(ctx, p.Info)
	}

	return p.out.RemoveStream(url)
}

func (p *Pipeline) SendEOS(ctx context.Context) {
	ctx, span := tracer.Start(ctx, "Pipeline.SendEOS")
	defer span.End()

	p.closed.Once(func() {
		if p.limitTimer != nil {
			p.limitTimer.Stop()
		}

		switch p.Info.Status {
		case livekit.EgressStatus_EGRESS_STARTING:
			p.Info.Status = livekit.EgressStatus_EGRESS_ABORTED
			fallthrough

		case livekit.EgressStatus_EGRESS_ABORTED,
			livekit.EgressStatus_EGRESS_FAILED:
			p.stop()

		case livekit.EgressStatus_EGRESS_ACTIVE:
			p.Info.Status = livekit.EgressStatus_EGRESS_ENDING
			p.Info.UpdatedAt = time.Now().UnixNano()
			p.sendUpdate(ctx, p.Info)
			fallthrough

		case livekit.EgressStatus_EGRESS_ENDING,
			livekit.EgressStatus_EGRESS_LIMIT_REACHED:
			go func() {
				logger.Infow("sending EOS to pipeline")

				p.eosTimer = time.AfterFunc(eosTimeout, func() {
					logger.Errorw("pipeline frozen", nil, "stream", p.StreamOnly)
					if p.Debug.EnableProfiling {
						p.uploadDebugFiles()
					}

					if p.StreamOnly {
						p.stop()
					} else {
						p.Failure <- errors.New("pipeline frozen")
					}
				})

				if p.SourceType == types.SourceTypeSDK {
					p.src.(*source.SDKSource).CloseWriters()
				}

				p.pipeline.SendEvent(gst.NewEOSEvent())
			}()
		}
	})
}

func (p *Pipeline) startSessionLimitTimer(ctx context.Context) {
	var timeout time.Duration
	for egressType := range p.Outputs {
		var t time.Duration
		switch egressType {
		case types.EgressTypeFile:
			t = p.FileOutputMaxDuration
		case types.EgressTypeStream, types.EgressTypeWebsocket:
			t = p.StreamOutputMaxDuration
		case types.EgressTypeSegments:
			t = p.SegmentOutputMaxDuration
		}
		if t > 0 && (timeout == 0 || t < timeout) {
			timeout = t
		}
	}

	if timeout > 0 {
		p.limitTimer = time.AfterFunc(timeout, func() {
			switch p.Info.Status {
			case livekit.EgressStatus_EGRESS_STARTING,
				livekit.EgressStatus_EGRESS_ACTIVE:
				p.Info.Status = livekit.EgressStatus_EGRESS_LIMIT_REACHED
			}
			p.SendEOS(ctx)
		})
	}
}

func (p *Pipeline) updateStartTime(startedAt int64) {
	for egressType, c := range p.Outputs {
		switch egressType {
		case types.EgressTypeStream, types.EgressTypeWebsocket:
			p.mu.Lock()
			for _, streamInfo := range c.(*config.StreamConfig).StreamInfo {
				streamInfo.Status = livekit.StreamInfo_ACTIVE
				streamInfo.StartedAt = startedAt
			}
			p.mu.Unlock()

		case types.EgressTypeFile:
			c.(*config.FileConfig).FileInfo.StartedAt = startedAt

		case types.EgressTypeSegments:
			c.(*config.SegmentConfig).SegmentsInfo.StartedAt = startedAt
		}
	}

	if p.Info.Status == livekit.EgressStatus_EGRESS_STARTING {
		p.Info.Status = livekit.EgressStatus_EGRESS_ACTIVE
		p.Info.UpdatedAt = time.Now().UnixNano()
		p.sendUpdate(context.Background(), p.Info)
	}
}

func (p *Pipeline) updateDuration(endedAt int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for egressType, c := range p.Outputs {
		switch egressType {
		case types.EgressTypeStream, types.EgressTypeWebsocket:
			for _, info := range c.(*config.StreamConfig).StreamInfo {
				info.Status = livekit.StreamInfo_FINISHED
				if info.StartedAt == 0 {
					info.StartedAt = endedAt
				}
				info.EndedAt = endedAt
				info.Duration = endedAt - info.StartedAt
			}

		case types.EgressTypeFile:
			fileInfo := c.(*config.FileConfig).FileInfo
			if fileInfo.StartedAt == 0 {
				fileInfo.StartedAt = endedAt
			}
			fileInfo.EndedAt = endedAt
			fileInfo.Duration = endedAt - fileInfo.StartedAt

		case types.EgressTypeSegments:
			segmentsInfo := c.(*config.SegmentConfig).SegmentsInfo
			if segmentsInfo.StartedAt == 0 {
				segmentsInfo.StartedAt = endedAt
			}
			segmentsInfo.EndedAt = endedAt
			segmentsInfo.Duration = endedAt - segmentsInfo.StartedAt
		}
	}
}

func (p *Pipeline) stop() {
	p.mu.Lock()

	if p.loop == nil {
		p.mu.Unlock()
		return
	}

	stateChange := make(chan error, 1)
	go func() {
		stateChange <- p.pipeline.BlockSetState(gst.StateNull)
	}()

	select {
	case err := <-stateChange:
		if err != nil {
			logger.Errorw("SetStateNull failed", err)
		}
	case <-time.After(eosTimeout):
		logger.Errorw("SetStateNull timed out", nil)
	}

	endedAt := time.Now().UnixNano()
	logger.Infow("pipeline stopped")

	p.loop.Quit()
	p.loop = nil
	p.mu.Unlock()

	if p.SourceType == types.SourceTypeWeb {
		p.updateDuration(endedAt)
	}
}
