package source

import (
	"context"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
)

type Source interface {
	StartRecording() chan struct{}
	EndRecording() chan struct{}
	Close()
}

func New(ctx context.Context, p *config.PipelineConfig) (Source, error) {
	switch p.RequestType {
	case types.RequestTypeRoomComposite,
		types.RequestTypeWeb:
		return NewWebSource(ctx, p)

	case types.RequestTypeParticipant,
		types.RequestTypeTrackComposite,
		types.RequestTypeTrack:
		return NewSDKSource(ctx, p)

	default:
		return nil, errors.ErrInvalidInput("request")
	}
}
