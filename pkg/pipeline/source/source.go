package source

import (
	"context"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/livekit"
)

type Source interface {
	StartRecording() chan struct{}
	EndRecording() chan struct{}
	Close()
}

func New(ctx context.Context, p *config.PipelineConfig) (Source, error) {
	switch p.Info.Request.(type) {
	case *livekit.EgressInfo_RoomComposite,
		*livekit.EgressInfo_Web:
		return NewWebSource(ctx, p)

	case *livekit.EgressInfo_TrackComposite,
		*livekit.EgressInfo_Track:
		return NewSDKSource(ctx, p)

	default:
		return nil, errors.ErrInvalidInput("request")
	}
}
