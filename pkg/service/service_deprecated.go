package service

import (
	"context"
	"time"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

func (s *Service) ListEgress() []string {
	return s.manager.listEgress()
}

func (s *Service) handleRequest(req *livekit.StartEgressRequest) {
	ctx, span := tracer.Start(context.Background(), "Service.handleRequest")
	defer span.End()

	if s.acceptRequest(ctx, req) {
		// validate before passing to handler
		p, err := config.GetValidatedPipelineConfig(s.conf, req)
		if err == nil {
			err = s.manager.launchHandler(req, 0)
		}

		s.sendResponse(ctx, req, p.Info, err)
		if err != nil {
			span.RecordError(err)
		}
	}
}

func (s *Service) acceptRequest(ctx context.Context, req *livekit.StartEgressRequest) bool {
	ctx, span := tracer.Start(ctx, "Service.acceptRequest")
	defer span.End()

	// check request time
	if time.Since(time.Unix(0, req.SentAt)) >= egress.RequestExpiration {
		return false
	}

	// check cpu load
	if !s.monitor.CanAcceptRequest(req) {
		return false
	}

	// claim request
	claimed, err := s.rpcServerV0.ClaimRequest(context.Background(), req)
	if err != nil {
		return false
	} else if !claimed {
		return false
	}

	// accept request
	s.monitor.AcceptRequest(req)
	logger.Infow("request accepted",
		"egressID", req.EgressId,
		"requestID", req.RequestId,
		"senderID", req.SenderId,
	)

	return true
}

func (s *Service) sendResponse(ctx context.Context, req *livekit.StartEgressRequest, info *livekit.EgressInfo, err error) {
	if err != nil {
		logger.Infow("bad request",
			"error", err,
			"egressID", info.EgressId,
			"requestID", req.RequestId,
			"senderID", req.SenderId,
		)
	}

	if err = s.rpcServerV0.SendResponse(ctx, req, info, err); err != nil {
		logger.Errorw("failed to send response", err)
	}
}
