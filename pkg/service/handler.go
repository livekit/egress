package service

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/egress/pkg/pipeline/params"
)

type Handler struct {
	conf      *config.Config
	rpcServer egress.RPCServer
	kill      chan struct{}
}

func NewHandler(conf *config.Config, rpcServer egress.RPCServer) *Handler {
	return &Handler{
		conf:      conf,
		rpcServer: rpcServer,
		kill:      make(chan struct{}),
	}
}

func (h *Handler) HandleRequest(req *livekit.StartEgressRequest) {
	// build/verify params
	pipelineParams, err := params.GetPipelineParams(h.conf, req)
	info := pipelineParams.Info
	if err != nil {
		info.Error = err.Error()
		info.Status = livekit.EgressStatus_EGRESS_FAILED
		h.sendUpdate(info)
		return
	}

	// create the pipeline
	p, err := pipeline.New(h.conf, pipelineParams)
	if err != nil {
		info.Error = err.Error()
		info.Status = livekit.EgressStatus_EGRESS_FAILED
		h.sendUpdate(info)
		return
	}

	p.OnStatusUpdate(h.sendUpdate)

	// subscribe to request channel
	requests, err := h.rpcServer.EgressSubscription(context.Background(), p.GetInfo().EgressId)
	if err != nil {
		return
	}
	defer func() {
		err := requests.Close()
		if err != nil {
			logger.Errorw("failed to unsubscribe from request channel", err)
		}
	}()

	// start egress
	result := make(chan *livekit.EgressInfo, 1)
	go func() {
		result <- p.Run()
	}()

	for {
		select {
		case <-h.kill:
			// kill signal received
			p.SendEOS()

		case res := <-result:
			// recording finished
			h.sendUpdate(res)
			return

		case msg := <-requests.Channel():
			// request received
			request := &livekit.EgressRequest{}
			err = proto.Unmarshal(requests.Payload(msg), request)
			if err != nil {
				logger.Errorw("failed to read request", err, "egressID", p.GetInfo().EgressId)
				continue
			}
			logger.Debugw("handling request", "egressID", p.GetInfo().EgressId, "requestID", request.RequestId)

			switch req := request.Request.(type) {
			case *livekit.EgressRequest_UpdateStream:
				err = p.UpdateStream(req.UpdateStream)
			case *livekit.EgressRequest_Stop:
				p.SendEOS()
			default:
				err = errors.ErrInvalidRPC
			}

			h.sendResponse(request, p.GetInfo(), err)
		}
	}
}

func (h *Handler) sendUpdate(info *livekit.EgressInfo) {
	switch info.Status {
	case livekit.EgressStatus_EGRESS_FAILED:
		logger.Errorw("egress failed", errors.New(info.Error), "egressID", info.EgressId)
	case livekit.EgressStatus_EGRESS_COMPLETE:
		logger.Infow("egress completed", "egressID", info.EgressId)
	default:
		logger.Infow("egress updated", "egressID", info.EgressId, "status", info.Status)
	}

	if err := h.rpcServer.SendUpdate(context.Background(), info); err != nil {
		logger.Errorw("failed to send update", err)
	}
}

func (h *Handler) sendResponse(req *livekit.EgressRequest, info *livekit.EgressInfo, err error) {
	args := []interface{}{
		"egressID", info.EgressId,
		"requestID", req.RequestId,
		"senderID", req.SenderId,
	}

	if err != nil {
		logger.Errorw("request failed", err, args...)
	} else {
		logger.Debugw("request handled", args...)
	}

	if err := h.rpcServer.SendResponse(context.Background(), req, info, err); err != nil {
		logger.Errorw("failed to send response", err, args...)
	}
}

func (h *Handler) Kill() {
	select {
	case <-h.kill:
		return
	default:
		close(h.kill)
	}
}
