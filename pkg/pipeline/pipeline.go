package pipeline

import (
	"context"
	"sync"
	"time"

	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"

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
)

const (
	pipelineSource = "pipeline"
	eosTimeout     = time.Second * 30
)

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
	closed     chan struct{}
	closeOnce  sync.Once
	eosTimer   *time.Timer

	// callbacks
	onStatusUpdate func(context.Context, *livekit.EgressInfo)
}

func New(ctx context.Context, p *config.PipelineConfig) (*Pipeline, error) {
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
	pipeline, err := gst.NewPipeline("pipeline")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	// create input bin
	in, err := input.New(ctx, pipeline, p)
	if err != nil {
		return nil, err
	}

	// create output bin
	out, err := output.New(ctx, pipeline, p)
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

	// set websocketSink callback with SDK source
	if s, ok := sinks[types.EgressTypeWebsocket]; ok {
		websocketSink := s.(*sink.WebsocketSink)
		src.(*source.SDKSource).OnTrackMuted(websocketSink.OnTrackMuted)
		if err = out.SetWebsocketSink(websocketSink); err != nil {
			return nil, err
		}
	}

	return &Pipeline{
		PipelineConfig: p,
		src:            src,
		pipeline:       pipeline,
		in:             in,
		out:            out,
		sinks:          sinks,
		closed:         make(chan struct{}),
	}, nil
}

func (p *Pipeline) OnStatusUpdate(f func(context.Context, *livekit.EgressInfo)) {
	p.onStatusUpdate = f
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

		switch p.Info.Status {
		case livekit.EgressStatus_EGRESS_STARTING,
			livekit.EgressStatus_EGRESS_ACTIVE,
			livekit.EgressStatus_EGRESS_ENDING:
			p.Info.Status = livekit.EgressStatus_EGRESS_COMPLETE
		}

		for _, s := range p.sinks {
			s.Cleanup()
		}
	}()

	// wait until room is ready
	start := p.src.StartRecording()
	if start != nil {
		select {
		case <-p.closed:
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

	// session limit timer
	p.startSessionLimitTimer(ctx)

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

	// skip upload if there was an error
	if p.Info.Error == "" {
		for _, s := range p.sinks {
			if err := s.Close(); err != nil {
				p.Info.Error = err.Error()
			}
		}
	}

	return p.Info
}

func (p *Pipeline) UpdateStream(ctx context.Context, req *livekit.UpdateStreamRequest) error {
	ctx, span := tracer.Start(ctx, "Pipeline.UpdateStream")
	defer span.End()

	for _, url := range req.AddOutputUrls {
		if err := p.VerifyUrl(url, types.OutputTypeRTMP); err != nil {
			return err
		}
	}

	errs := errors.ErrArray{}

	now := time.Now().UnixNano()
	for _, url := range req.AddOutputUrls {
		if err := p.out.AddStream(url); err != nil {
			errs.AppendErr(err)
			continue
		}

		p.mu.Lock()
		streamInfo := &livekit.StreamInfo{
			Url:       url,
			StartedAt: now,
			Status:    livekit.StreamInfo_ACTIVE,
		}
		p.Outputs[types.EgressTypeStream].StreamInfo[url] = streamInfo
		p.Info.StreamResults = append(p.Info.StreamResults, streamInfo)
		if streamInfoDeprecated := p.Info.GetStream(); streamInfoDeprecated != nil {
			streamInfoDeprecated.Info = append(streamInfoDeprecated.Info, streamInfo)
		}
		p.mu.Unlock()
	}

	for _, url := range req.RemoveOutputUrls {
		if err := p.removeSink(url, livekit.StreamInfo_FINISHED); err != nil {
			errs.AppendErr(err)
		}
	}

	return errs.ToError()
}

func (p *Pipeline) removeSink(url string, status livekit.StreamInfo_Status) error {
	now := time.Now().UnixNano()

	p.mu.Lock()
	streamInfo := p.Outputs[types.EgressTypeStream].StreamInfo[url]
	streamInfo.Status = status
	streamInfo.EndedAt = now
	if streamInfo.StartedAt == 0 {
		streamInfo.StartedAt = now
	} else {
		streamInfo.Duration = now - streamInfo.StartedAt
	}
	delete(p.Outputs[types.EgressTypeStream].StreamInfo, url)
	done := len(p.Outputs[types.EgressTypeStream].StreamInfo) == 0
	p.mu.Unlock()

	logger.Debugw("removing stream sink", "url", url, "status", status, "duration", streamInfo.Duration)

	switch status {
	case livekit.StreamInfo_FINISHED:
		if done {
			p.SendEOS(context.Background())
			return nil
		}
	case livekit.StreamInfo_FAILED:
		if done {
			return errors.ErrFailedToConnect
		} else if p.onStatusUpdate != nil {
			p.onStatusUpdate(context.Background(), p.Info)
		}
	}

	return p.out.RemoveStream(url)
}

func (p *Pipeline) GetGstPipelineDebugDot() (string, error) {
	s := p.pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
	return s, nil
}

func (p *Pipeline) SendEOS(ctx context.Context) {
	ctx, span := tracer.Start(ctx, "Pipeline.SendEOS")
	defer span.End()

	p.closeOnce.Do(func() {
		p.close(ctx)

		go func() {
			logger.Debugw("sending EOS to pipeline")
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
			p.SendEOS(ctx)
			p.Info.Status = livekit.EgressStatus_EGRESS_LIMIT_REACHED
		})
	}
}

func (p *Pipeline) updateStartTime(startedAt int64) {
	for egressType, conf := range p.Outputs {
		switch egressType {
		case types.EgressTypeStream, types.EgressTypeWebsocket:
			p.mu.Lock()
			for _, streamInfo := range conf.StreamInfo {
				streamInfo.Status = livekit.StreamInfo_ACTIVE
				streamInfo.StartedAt = startedAt
			}
			p.mu.Unlock()

		case types.EgressTypeFile:
			conf.FileInfo.StartedAt = startedAt

		case types.EgressTypeSegments:
			conf.SegmentsInfo.StartedAt = startedAt
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

	for egressType, conf := range p.Outputs {
		switch egressType {
		case types.EgressTypeStream, types.EgressTypeWebsocket:
			for _, info := range conf.StreamInfo {
				info.Status = livekit.StreamInfo_FINISHED
				if info.StartedAt == 0 {
					info.StartedAt = endedAt
				}
				info.EndedAt = endedAt
				info.Duration = endedAt - info.StartedAt
			}

		case types.EgressTypeFile:
			if conf.FileInfo.StartedAt == 0 {
				conf.FileInfo.StartedAt = endedAt
			}
			conf.FileInfo.EndedAt = endedAt
			conf.FileInfo.Duration = endedAt - conf.FileInfo.StartedAt

		case types.EgressTypeSegments:
			if conf.SegmentsInfo.StartedAt == 0 {
				conf.SegmentsInfo.StartedAt = endedAt
			}
			conf.SegmentsInfo.EndedAt = endedAt
			conf.SegmentsInfo.Duration = endedAt - conf.SegmentsInfo.StartedAt
		}
	}
}

func (p *Pipeline) close(ctx context.Context) {
	close(p.closed)
	if p.limitTimer != nil {
		p.limitTimer.Stop()
	}

	switch p.Info.Status {
	case livekit.EgressStatus_EGRESS_STARTING,
		livekit.EgressStatus_EGRESS_ACTIVE:

		p.Info.Status = livekit.EgressStatus_EGRESS_ENDING
		if p.onStatusUpdate != nil {
			p.onStatusUpdate(ctx, p.Info)
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
	logger.Debugw("pipeline stopped")

	p.loop.Quit()
	p.loop = nil
	p.mu.Unlock()

	if p.SourceType == types.SourceTypeWeb {
		p.updateDuration(endedAt)
	}
}
