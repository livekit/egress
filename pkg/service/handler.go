package service

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/egress/pkg/pipeline/params"
)

type Handler struct {
	ctx  context.Context
	conf *config.Config
	bus  utils.MessageBus
	kill chan struct{}
}

func NewHandler(conf *config.Config, bus utils.MessageBus) *Handler {
	return &Handler{
		ctx:  context.Background(),
		conf: conf,
		bus:  bus,
		kill: make(chan struct{}),
	}
}

func (h *Handler) HandleRequest(req *livekit.StartEgressRequest) {
	// build/verify params
	pipelineParams, err := params.GetPipelineParams(h.conf, req)
	info := pipelineParams.Info
	if err != nil {
		info.Error = err.Error()
		info.Status = livekit.EgressStatus_EGRESS_COMPLETE
		h.sendRPCResponse(req.RequestId, pipelineParams.Info, err)
		return
	}

	h.sendRPCResponse(req.RequestId, pipelineParams.Info, nil)

	// create the pipeline
	p, err := pipeline.New(h.conf, pipelineParams)
	if err != nil {
		info.Error = err.Error()
		info.Status = livekit.EgressStatus_EGRESS_COMPLETE
		h.sendEgressUpdate(info)
		return
	}

	p.OnStatusUpdate(h.sendEgressUpdate)

	// subscribe to request channel
	requests, err := h.bus.Subscribe(h.ctx, egress.RequestChannel(p.GetInfo().EgressId))
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
			h.sendEgressUpdate(res)
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

			h.sendRPCResponse(request.RequestId, p.GetInfo(), err)
		}
	}
}

func (h *Handler) sendRPCResponse(requestID string, info *livekit.EgressInfo, err error) {
	res := &livekit.EgressResponse{Info: info}
	args := []interface{}{"egressID", info.EgressId, "requestId", requestID}

	if err != nil {
		logger.Errorw("error handling request", err, args...)
		res.Error = err.Error()
	} else {
		logger.Debugw("request handled", args...)
	}

	if err = h.bus.Publish(h.ctx, egress.ResponseChannel(requestID), res); err != nil {
		logger.Errorw("could not send response", err)
	}
}

func (h *Handler) sendEgressUpdate(info *livekit.EgressInfo) {
	if info.Error != "" {
		logger.Errorw("egress failed", errors.New(info.Error), "egressID", info.EgressId)
	} else if info.Status == livekit.EgressStatus_EGRESS_COMPLETE {
		logger.Infow("egress completed successfully", "egressID", info.EgressId)
	} else {
		logger.Infow("egress updated", "egressID", info.EgressId, "status", info.Status)
	}

	if err := h.bus.Publish(h.ctx, egress.ResultsChannel, info); err != nil {
		logger.Errorw("failed to send egress update", err)
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
