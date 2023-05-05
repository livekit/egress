package pipeline

import (
	"context"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/input"
	"github.com/livekit/egress/pkg/pipeline/output"
	"github.com/livekit/egress/pkg/pipeline/sink"
	"github.com/livekit/egress/pkg/pipeline/source"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/egress/pkg/util"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
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
	playing    bool
	limitTimer *time.Timer
	closed     core.Fuse
	eosTimer   *time.Timer

	// callbacks
	onStatusUpdate UpdateFunc
}

func New(ctx context.Context, p *config.PipelineConfig, onStatusUpdate UpdateFunc) (*Pipeline, error) {
	ctx, span := tracer.Start(ctx, "Pipeline.New")
	defer span.End()

	// initialize gst
	go func() {
		_, span := tracer.Start(ctx, "gst.Init")
		defer span.End()
		gst.Init(nil)
		close(p.GstReady)
	}()

	// create source
	src, err := source.New(ctx, p)
	if err != nil {
		return nil, err
	}

	// create pipeline
	<-p.GstReady
	gp, err := gst.NewPipeline("pipeline")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	// create input bin
	in, err := input.New(ctx, gp, p)
	if err != nil {
		return nil, err
	}

	// create output bin
	out, err := output.New(ctx, gp, p)
	if err != nil {
		return nil, err
	}

	// link input bin
	audioSrcPad, videoSrcPad, err := in.Link()
	if err != nil {
		return nil, err
	}

	// link output bin
	if err = out.Link(audioSrcPad, videoSrcPad); err != nil {
		return nil, err
	}

	// create sinks
	sinks, err := sink.CreateSinks(p)
	if err != nil {
		return nil, err
	}

	pipeline := &Pipeline{
		PipelineConfig: p,
		src:            src,
		pipeline:       gp,
		in:             in,
		out:            out,
		sinks:          sinks,
		closed:         core.NewFuse(),
		onStatusUpdate: onStatusUpdate,
	}

	// set websocketSink callback with SDK source
	if s, ok := sinks[types.EgressTypeSegments]; ok {
		segmentSink := s.(*sink.SegmentSink)
		segmentSink.SetOnFailure(func(err error) {
			pipeline.Info.Error = err.Error()
			pipeline.stop()
		})
	}
	if s, ok := sinks[types.EgressTypeWebsocket]; ok {
		websocketSink := s.(*sink.WebsocketSink)
		src.(*source.SDKSource).OnTrackMuted(websocketSink.OnTrackMuted)
		if err = out.SetWebsocketSink(websocketSink); err != nil {
			return nil, err
		}
	}

	return pipeline, nil
}

func (p *Pipeline) Run(ctx context.Context) *livekit.EgressInfo {
	ctx, span := tracer.Start(ctx, "Pipeline.Run")
	defer span.End()

	p.Info.StartedAt = time.Now().UnixNano()
	defer func() {
		p.Info.EndedAt = time.Now().UnixNano()

		// update status
		if p.Info.Error != "" {
			p.Info.Status = livekit.EgressStatus_EGRESS_FAILED
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

	// close when room ends
	go func() {
		<-p.src.EndRecording()
		p.SendEOS(ctx)
	}()

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

	errs := errors.ErrArray{}
	now := time.Now().UnixNano()

	// add stream outputs first
	for _, url := range req.AddOutputUrls {
		// validate and redact url
		redacted, err := p.ValidateUrl(url, types.OutputTypeRTMP)
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
	}

	// remove stream outputs
	for _, url := range req.RemoveOutputUrls {
		if err := p.removeSink(url, nil); err != nil {
			errs.AppendErr(err)
		}
	}

	return errs.ToError()
}

func (p *Pipeline) removeSink(url string, streamErr error) error {
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
	redacted, _ := util.RedactStreamKey(url)
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
			p.SendEOS(context.Background())
			return nil
		}
	}

	// only send updates if the egress will continue, otherwise it's handled by UpdateStream RPC
	if streamErr != nil && p.onStatusUpdate != nil {
		p.onStatusUpdate(context.Background(), p.Info)
	}

	return p.out.RemoveStream(url)
}

func (p *Pipeline) GetGstPipelineDebugDot() string {
	return p.pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
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
			if p.onStatusUpdate != nil {
				p.onStatusUpdate(ctx, p.Info)
			}
			fallthrough

		case livekit.EgressStatus_EGRESS_ENDING,
			livekit.EgressStatus_EGRESS_LIMIT_REACHED:
			go func() {
				logger.Infow("sending EOS to pipeline")

				p.eosTimer = time.AfterFunc(eosTimeout, func() {
					logger.Errorw("pipeline frozen", nil)
					p.Info.Error = "pipeline frozen"
					p.stop()
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
		if p.onStatusUpdate != nil {
			p.onStatusUpdate(context.Background(), p.Info)
		}
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

	_ = p.pipeline.BlockSetState(gst.StateNull)
	endedAt := time.Now().UnixNano()
	logger.Infow("pipeline stopped")

	p.loop.Quit()
	p.loop = nil
	p.mu.Unlock()

	if p.SourceType == types.SourceTypeWeb {
		p.updateDuration(endedAt)
	}
}
