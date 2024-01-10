package service

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

type IOClient struct {
	rpc.IOInfoClient
}

func NewIOClient(bus psrpc.MessageBus) (rpc.IOInfoClient, error) {
	client, err := rpc.NewIOInfoClient(bus)
	if err != nil {
		return nil, err
	}
	return &IOClient{
		IOInfoClient: client,
	}, nil
}

func (c *IOClient) CreateEgress(ctx context.Context, info *livekit.EgressInfo, opts ...psrpc.RequestOption) (*emptypb.Empty, error) {
	_, err := c.IOInfoClient.CreateEgress(ctx, info, opts...)
	if err != nil {
		logger.Errorw("failed to create egress", err)
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (c *IOClient) UpdateEgress(ctx context.Context, info *livekit.EgressInfo, opts ...psrpc.RequestOption) (*emptypb.Empty, error) {
	_, err := c.IOInfoClient.UpdateEgress(ctx, info, opts...)
	if err != nil {
		logger.Errorw("failed to update egress", err)
		return nil, err
	}

	requestType, outputType := egress.GetTypes(info.Request)
	switch info.Status {
	case livekit.EgressStatus_EGRESS_FAILED:
		logger.Warnw("egress failed", errors.New(info.Error),
			"egressID", info.EgressId,
			"requestType", requestType,
			"outputType", outputType,
		)
	case livekit.EgressStatus_EGRESS_COMPLETE:
		logger.Infow("egress completed",
			"egressID", info.EgressId,
			"requestType", requestType,
			"outputType", outputType,
		)
	default:
		logger.Infow("egress updated",
			"egressID", info.EgressId,
			"requestType", requestType,
			"outputType", outputType,
			"status", info.Status,
		)
	}

	return &emptypb.Empty{}, nil
}

func (c *IOClient) UpdateMetrics(ctx context.Context, req *rpc.UpdateMetricsRequest, opts ...psrpc.RequestOption) (*emptypb.Empty, error) {
	_, err := c.IOInfoClient.UpdateMetrics(ctx, req, opts...)
	if err != nil {
		logger.Errorw("failed to update metrics", err)
		return nil, err
	}
	return &emptypb.Empty{}, nil
}
