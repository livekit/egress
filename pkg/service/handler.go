package service

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

type Handler struct {
	conf      *config.PipelineConfig
	rpcServer egress.RPCServer
	kill      chan struct{}
}

func NewHandler(conf *config.PipelineConfig, rpcServer egress.RPCServer) *Handler {
	return &Handler{
		conf:      conf,
		rpcServer: rpcServer,
		kill:      make(chan struct{}),
	}
}

func (h *Handler) Run() error {
	ctx, span := tracer.Start(context.Background(), "Handler.Run")
	defer span.End()

	p, err := h.buildPipeline(ctx)
	if err != nil {
		span.RecordError(err)
		if errors.IsFatal(err) {
			return err
		} else {
			return nil
		}
	}

	// subscribe to request channel
	requests, err := h.rpcServer.EgressSubscription(context.Background(), p.Info.EgressId)
	if err != nil {
		span.RecordError(err)
		logger.Errorw("failed to subscribe to egress", err)
		return nil
	}
	defer func() {
		if err := requests.Close(); err != nil {
			logger.Errorw("failed to unsubscribe from request channel", err)
		}
	}()

	// start egress
	result := make(chan *livekit.EgressInfo, 1)
	go func() {
		result <- p.Run(ctx)
	}()

	for {
		select {
		case <-h.kill:
			// kill signal received
			p.SendEOS(ctx)

		case res := <-result:
			// recording finished
			h.sendUpdate(ctx, res)
			return nil

		case msg := <-requests.Channel():
			// request received
			request := &livekit.EgressRequest{}
			err = proto.Unmarshal(requests.Payload(msg), request)
			if err != nil {
				logger.Errorw("failed to read request", err, "egressID", p.Info.EgressId)
				continue
			}
			logger.Debugw("handling request", "egressID", p.Info.EgressId, "requestID", request.RequestId)

			switch r := request.Request.(type) {
			case *livekit.EgressRequest_UpdateStream:
				err = p.UpdateStream(ctx, r.UpdateStream)
			case *livekit.EgressRequest_Stop:
				p.SendEOS(ctx)
			default:
				err = errors.ErrInvalidRPC
			}

			h.sendResponse(ctx, request, p.Info, err)
		}
	}
}

func (h *Handler) buildPipeline(ctx context.Context) (*pipeline.Pipeline, error) {
	ctx, span := tracer.Start(ctx, "Handler.buildPipeline")
	defer span.End()

	// build/verify params
	p, err := pipeline.New(ctx, h.conf)
	if err != nil {
		h.conf.Info.Error = err.Error()
		h.conf.Info.Status = livekit.EgressStatus_EGRESS_FAILED
		h.sendUpdate(ctx, h.conf.Info)
		span.RecordError(err)
		return nil, err
	}

	p.OnStatusUpdate(h.sendUpdate)
	return p, nil
}

func (h *Handler) sendUpdate(ctx context.Context, info *livekit.EgressInfo) {
	requestType, outputType := getTypes(info)
	switch info.Status {
	case livekit.EgressStatus_EGRESS_FAILED:
		logger.Warnw("egress failed", errors.New(info.Error),
			"egressID", info.EgressId,
			"request_type", requestType,
			"output_type", outputType,
		)
	case livekit.EgressStatus_EGRESS_COMPLETE:
		logger.Infow("egress completed",
			"egressID", info.EgressId,
			"request_type", requestType,
			"output_type", outputType,
		)
	default:
		logger.Infow("egress updated",
			"egressID", info.EgressId,
			"request_type", requestType,
			"output_type", outputType,
			"status", info.Status,
		)
	}

	if err := h.rpcServer.SendUpdate(ctx, info); err != nil {
		logger.Errorw("failed to send update", err)
	}
}

func (h *Handler) sendResponse(ctx context.Context, req *livekit.EgressRequest, info *livekit.EgressInfo, err error) {
	args := []interface{}{
		"egressID", info.EgressId,
		"requestID", req.RequestId,
		"senderID", req.SenderId,
	}

	if err != nil {
		logger.Warnw("request failed", err, args...)
	} else {
		logger.Debugw("request handled", args...)
	}

	if err = h.rpcServer.SendResponse(ctx, req, info, err); err != nil {
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

func getTypes(info *livekit.EgressInfo) (requestType string, outputType string) {
	switch req := info.Request.(type) {
	case *livekit.EgressInfo_RoomComposite:
		requestType = "room_composite"
		switch req.RoomComposite.Output.(type) {
		case *livekit.RoomCompositeEgressRequest_File:
			outputType = "file"
		case *livekit.RoomCompositeEgressRequest_Stream:
			outputType = "stream"
		case *livekit.RoomCompositeEgressRequest_Segments:
			outputType = "segments"
		}
	case *livekit.EgressInfo_Web:
		requestType = "web"
		switch req.Web.Output.(type) {
		case *livekit.WebEgressRequest_File:
			outputType = "file"
		case *livekit.WebEgressRequest_Stream:
			outputType = "stream"
		case *livekit.WebEgressRequest_Segments:
			outputType = "segments"
		}
	case *livekit.EgressInfo_TrackComposite:
		requestType = "track_composite"
		switch req.TrackComposite.Output.(type) {
		case *livekit.TrackCompositeEgressRequest_File:
			outputType = "file"
		case *livekit.TrackCompositeEgressRequest_Stream:
			outputType = "stream"
		case *livekit.TrackCompositeEgressRequest_Segments:
			outputType = "segments"
		}
	case *livekit.EgressInfo_Track:
		requestType = "track"
		switch req.Track.Output.(type) {
		case *livekit.TrackEgressRequest_File:
			outputType = "file"
		case *livekit.TrackEgressRequest_WebsocketUrl:
			outputType = "websocket"
		}
	}

	return
}
