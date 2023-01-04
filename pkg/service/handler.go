package service

import (
	"context"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/egress/pkg/pprof"
	"github.com/livekit/livekit-server/pkg/service/rpc"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/psrpc"
)

const network = "unix"

type Handler struct {
	ipc.UnimplementedEgressHandlerServer

	conf       *config.PipelineConfig
	pipeline   *pipeline.Pipeline
	rpcServer  rpc.EgressHandlerServer
	grpcServer *grpc.Server
	kill       chan struct{}
}

func NewHandler(conf *config.PipelineConfig, bus psrpc.MessageBus) (*Handler, error) {
	h := &Handler{
		conf:       conf,
		grpcServer: grpc.NewServer(),
		kill:       make(chan struct{}),
	}

	rpcServer, err := rpc.NewEgressHandlerServer(conf.HandlerID, h, bus)
	if err != nil {
		return nil, err
	}
	if err = rpcServer.RegisterUpdateStreamTopic(conf.Info.EgressId); err != nil {
		return nil, err
	}
	if err = rpcServer.RegisterStopEgressTopic(conf.Info.EgressId); err != nil {
		return nil, err
	}
	h.rpcServer = rpcServer

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
	h.pipeline = p

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
			h.rpcServer.Shutdown()
			h.grpcServer.Stop()
			return nil
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

func (h *Handler) UpdateStream(ctx context.Context, req *livekit.UpdateStreamRequest) (*livekit.EgressInfo, error) {
	ctx, span := tracer.Start(ctx, "Handler.UpdateStream")
	defer span.End()

	err := h.pipeline.UpdateStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return h.pipeline.Info, nil
}

func (h *Handler) StopEgress(ctx context.Context, _ *livekit.StopEgressRequest) (*livekit.EgressInfo, error) {
	ctx, span := tracer.Start(ctx, "Handler.StopEgress")
	defer span.End()

	h.pipeline.SendEOS(ctx)
	return h.pipeline.Info, nil
}

type dotResponse struct {
	dot string
	err error
}

func (h *Handler) GetPipelineDot(ctx context.Context, _ *ipc.GstPipelineDebugDotRequest) (*ipc.GstPipelineDebugDotResponse, error) {
	ctx, span := tracer.Start(ctx, "Handler.GetPipelineDot")
	defer span.End()

	res := make(chan *dotResponse, 1)
	go func() {
		dot, err := h.pipeline.GetGstPipelineDebugDot()
		res <- &dotResponse{
			dot: dot,
			err: err,
		}
	}()

	select {
	case r := <-res:
		if r.err != nil {
			return nil, r.err
		}
		return &ipc.GstPipelineDebugDotResponse{
			DotFile: r.dot,
		}, nil

	case <-time.After(2 * time.Second):
		return nil, status.New(codes.DeadlineExceeded, "timed out requesting pipeline debug info").Err()
	}
}

func (h *Handler) GetPProf(ctx context.Context, req *ipc.PProfRequest) (*ipc.PProfResponse, error) {
	ctx, span := tracer.Start(ctx, "Handler.GetPProf")
	defer span.End()

	b, err := pprof.GetProfileData(ctx, req.ProfileName, int(req.Timeout), int(req.Debug))
	if err != nil {
		return nil, err
	}

	return &ipc.PProfResponse{
		PprofFile: b,
	}, nil
}

func (h *Handler) Kill() {
	select {
	case <-h.kill:
		return
	default:
		close(h.kill)
	}
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

	if err := h.rpcServer.PublishInfoUpdate(ctx, info); err != nil {
		logger.Errorw("failed to send update", err)
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
