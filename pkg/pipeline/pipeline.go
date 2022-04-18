package pipeline

import (
	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/composite"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/livekit-egress/pkg/pipeline/track"
)

type Pipeline interface {
	Info() *livekit.EgressInfo
	Run() *livekit.EgressInfo
	UpdateStream(req *livekit.UpdateStreamRequest) error
	Stop()
}

func FromRequest(conf *config.Config, request *livekit.StartEgressRequest) (Pipeline, error) {
	// get params
	p, err := params.GetPipelineParams(conf, request)
	if err != nil {
		return nil, err
	}

	return FromParams(conf, p)
}

func FromParams(conf *config.Config, p *params.Params) (Pipeline, error) {
	switch p.Info.Request.(type) {
	case *livekit.EgressInfo_RoomComposite:
		return composite.NewPipeline(conf, p)
	case *livekit.EgressInfo_TrackComposite:
		return composite.NewPipeline(conf, p)
	case *livekit.EgressInfo_Track:
		return track.NewPipeline(p)
	default:
		return nil, errors.ErrInvalidInput("request")
	}
}
