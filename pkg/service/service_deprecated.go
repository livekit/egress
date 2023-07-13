package service

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
)

func (s *Service) runV0() error {
	requests, err := s.rpcServerV0.GetRequestChannel(context.Background())
	if err != nil {
		return err
	}

	defer func() {
		_ = requests.Close()
	}()

	logger.Debugw("service ready")

	shutdown := s.shutdown.Watch()
	for {
		select {
		case <-shutdown:
			logger.Infow("shutting down")
			s.psrpcServer.DeregisterStartEgressTopic(s.conf.ClusterID)
			for !s.IsIdle() {
				time.Sleep(shutdownTimer)
			}
			s.psrpcServer.Shutdown()
			return nil

		case msg := <-requests.Channel():
			logger.Warnw("Using deprecated egress client. Upgrade livekit-server to v1.3.5+ and add egress:use_psrpc:true to your livekit config", nil)

			deprecated := &livekit.StartEgressRequest{}
			if err = proto.Unmarshal(requests.Payload(msg), deprecated); err != nil {
				logger.Errorw("malformed request", err)
				continue
			}

			req := &rpc.StartEgressRequest{
				EgressId: deprecated.EgressId,
				RoomId:   deprecated.RoomId,
				Token:    deprecated.Token,
				WsUrl:    deprecated.WsUrl,
			}
			switch r := deprecated.Request.(type) {
			case *livekit.StartEgressRequest_RoomComposite:
				req.Request = &rpc.StartEgressRequest_RoomComposite{RoomComposite: r.RoomComposite}
			case *livekit.StartEgressRequest_Web:
				req.Request = &rpc.StartEgressRequest_Web{Web: r.Web}
			case *livekit.StartEgressRequest_TrackComposite:
				req.Request = &rpc.StartEgressRequest_TrackComposite{TrackComposite: r.TrackComposite}
			case *livekit.StartEgressRequest_Track:
				req.Request = &rpc.StartEgressRequest_Track{Track: r.Track}
			}

			s.handleRequestV0(req, deprecated)
		}
	}
}

func (s *Service) handleRequestV0(req *rpc.StartEgressRequest, deprecated *livekit.StartEgressRequest) {
	ctx, span := tracer.Start(context.Background(), "Service.handleRequest")
	defer span.End()

	if s.acceptRequestV0(ctx, req, deprecated) {
		// validate before passing to handler
		p, err := config.GetValidatedPipelineConfig(s.conf, req)
		if err == nil {
			err = s.launchHandler(req, p.Info, 0)
		}

		s.sendResponseV0(ctx, deprecated, p.Info, err)
		if err != nil {
			span.RecordError(err)
		}
	}
}

func (s *Service) acceptRequestV0(ctx context.Context, req *rpc.StartEgressRequest, deprecated *livekit.StartEgressRequest) bool {
	ctx, span := tracer.Start(ctx, "Service.acceptRequest")
	defer span.End()

	// check request time
	if time.Since(time.Unix(0, deprecated.SentAt)) >= egress.RequestExpiration {
		return false
	}

	// check cpu load
	if !s.CanAcceptRequest(req) {
		return false
	}

	// claim request
	claimed, err := s.rpcServerV0.ClaimRequest(context.Background(), deprecated)
	if err != nil {
		return false
	} else if !claimed {
		return false
	}

	// accept request
	_ = s.AcceptRequest(req)
	logger.Infow("request accepted",
		"egressID", req.EgressId,
		"requestID", deprecated.RequestId,
		"senderID", deprecated.SenderId,
	)

	return true
}

func (s *Service) sendResponseV0(ctx context.Context, deprecated *livekit.StartEgressRequest, info *livekit.EgressInfo, err error) {
	if err != nil {
		logger.Infow("bad request",
			"error", err,
			"egressID", info.EgressId,
			"requestID", deprecated.RequestId,
			"senderID", deprecated.SenderId,
		)
	}

	if err = s.rpcServerV0.SendResponse(ctx, deprecated, info, err); err != nil {
		logger.Errorw("failed to send response", err)
	}
}
