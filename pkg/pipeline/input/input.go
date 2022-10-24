package input

import (
	"context"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/input/sdk"
	"github.com/livekit/egress/pkg/pipeline/input/web"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/livekit"
)

type Input interface {
	Bin() *gst.Bin
	Element() *gst.Element
	Link() error
	StartRecording() chan struct{}
	EndRecording() chan struct{}
	Close()
}

func New(ctx context.Context, conf *config.Config, p *params.Params) (Input, error) {
	switch p.Info.Request.(type) {
	case *livekit.EgressInfo_RoomComposite:
		return web.NewWebInput(ctx, conf, p)

	case *livekit.EgressInfo_TrackComposite,
		*livekit.EgressInfo_Track:
		return sdk.NewSDKInput(ctx, p)

	default:
		return nil, errors.ErrInvalidInput("request")
	}
}
