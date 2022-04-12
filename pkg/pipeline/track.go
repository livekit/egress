package pipeline

import (
	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/livekit-egress/pkg/pipeline/source"
)

type trackPipeline struct {
	source *source.SDKSource

	info   *livekit.EgressInfo
	closed chan struct{}
}

func NewTrackPipeline(p *params.Params) (*trackPipeline, error) {
	pipeline := &trackPipeline{
		info:   p.Info,
		closed: make(chan struct{}),
	}

	s, err := source.NewSDKFileSource(p)
	if err != nil {
		return nil, err
	}
	pipeline.source = s

	return pipeline, nil
}

func (p *trackPipeline) Info() *livekit.EgressInfo {
	return p.info
}

func (p *trackPipeline) Run() *livekit.EgressInfo {
	select {
	case <-p.source.EndRecording():
	case <-p.closed:
	}

	p.source.Close()
	return p.info
}

func (p *trackPipeline) UpdateStream(_ *livekit.UpdateStreamRequest) error {
	return errors.ErrInvalidRPC
}

func (p *trackPipeline) Stop() {
	select {
	case <-p.closed:
		return
	default:
		close(p.closed)
	}
}
