package pipeline

import (
	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/composite"
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
	params, err := config.GetPipelineParams(conf, request)
	if err != nil {
		return nil, err
	}

	return FromParams(conf, params)
}

func FromParams(conf *config.Config, params *config.Params) (Pipeline, error) {
	switch params.Info.Request.(type) {
	case *livekit.EgressInfo_WebComposite:
		return composite.NewPipeline(conf, params)
	case *livekit.EgressInfo_TrackComposite:
		return composite.NewPipeline(conf, params)
	case *livekit.EgressInfo_Track:
		return track.NewPipeline(params)
	default:
		return nil, errors.ErrInvalidInput
	}
}
