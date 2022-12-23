package service

import (
	"context"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

type HandlerV0 struct {
	ipc.UnimplementedEgressHandlerServer

	conf          *config.PipelineConfig
	rpcServer     egress.RPCServer
	grpcServer    *grpc.Server
	kill          chan struct{}
	debugRequests chan chan pipelineDebugResponse
}

func NewHandlerV0(conf *config.PipelineConfig, rpcServer egress.RPCServer) (*HandlerV0, error) {
	h := &HandlerV0{
		conf:          conf,
		rpcServer:     rpcServer,
		grpcServer:    grpc.NewServer(),
		kill:          make(chan struct{}),
		debugRequests: make(chan chan pipelineDebugResponse),
	}

	listener, err := net.Listen(network, getSocketAddress(conf.TmpDir))
	if err != nil {
		return nil, err
	}

	ipc.RegisterEgressHandlerServer(h.grpcServer, h)

	go func() {
		err := h.grpcServer.Serve(listener)
		if err != nil {
			logger.Errorw("failed to start grpc handler", err)
		}
	}()

	return h, nil
}

func (h *HandlerV0) Run() error {
	ctx, span := tracer.Start(context.Background(), "HandlerV0.Run")
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
		case debugResponseChan := <-h.debugRequests:
			dot, err := p.GetGstPipelineDebugDot()
			select {
			case debugResponseChan <- pipelineDebugResponse{dot: dot, err: err}:
			default:
				logger.Debugw("unable to return gstreamer debug dot file to caller")
			}
		}
	}
}

func (h *HandlerV0) GetDebugInfo(ctx context.Context, req *ipc.GetDebugInfoRequest) (*ipc.GetDebugInfoResponse, error) {
	ctx, span := tracer.Start(ctx, "HandlerV0.GetDebugInfo")
	defer span.End()

	switch req.Request.(type) {
	case *ipc.GetDebugInfoRequest_GstPipelineDot:
		pReq := make(chan pipelineDebugResponse, 1)

		h.debugRequests <- pReq

		select {
		case pResp := <-pReq:
			if pResp.err != nil {
				return nil, pResp.err
			}
			resp := &ipc.GetDebugInfoResponse{
				Response: &ipc.GetDebugInfoResponse_GstPipelineDot{
					GstPipelineDot: &ipc.GstPipelineDebugDotResponse{
						DotFile: pResp.dot,
					},
				},
			}
			return resp, nil

		case <-time.After(2 * time.Second):
			return nil, errors.New("timed out requesting pipeline debug info")
		}

	default:
		return nil, errors.New("unsupported debug info request type")
	}
}

func (h *HandlerV0) buildPipeline(ctx context.Context) (*pipeline.Pipeline, error) {
	ctx, span := tracer.Start(ctx, "HandlerV0.buildPipeline")
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

func (h *HandlerV0) sendUpdate(ctx context.Context, info *livekit.EgressInfo) {
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

func (h *HandlerV0) sendResponse(ctx context.Context, req *livekit.EgressRequest, info *livekit.EgressInfo, err error) {
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

func (h *HandlerV0) Kill() {
	select {
	case <-h.kill:
		return
	default:
		close(h.kill)
	}
}
