package service

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	gstPipelineDotFile = "gst_pipeline"
	pprof              = "pprof"
)

type handlerProxyHandler struct {
	processManager *ProcessManager
}

// URL path format is "/<application>/<egress_id>/<optional_other_params>"
func (p *handlerProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	pathElements := strings.Split(r.URL.Path, "/")
	if len(pathElements) < 3 {
		http.Error(w, "malformed url", http.StatusNotFound)
		return
	}

	app := pathElements[1]
	egressId := pathElements[2]

	grpcReq := &ipc.GetDebugInfoRequest{}
	switch app {
	case gstPipelineDotFile:
		grpcReq.Request = &ipc.GetDebugInfoRequest_GstPipelineDot{
			GstPipelineDot: &ipc.GstPipelineDebugDotRequest{},
		}
	case pprof:
		if len(pathElements) < 4 {
			http.Error(w, "missing profine name", http.StatusNotFound)
			return
		}

		timeout, _ := strconv.Atoi(r.URL.Query().Get("timeout"))

		grpcReq.Request = &ipc.GetDebugInfoRequest_Pprof{
			Pprof: &ipc.PprofRequest{
				ProfileName: pathElements[3],
				Timeout:     int32(timeout),
			},
		}
	default:
		http.Error(w, "unknown application", http.StatusNotFound)
		return
	}

	grpcResp, err := p.processManager.sendGrpcDebugRequest(egressId, grpcReq)
	statusErr, statusOk := err.(interface {
		GRPCStatus() *status.Status
	})

	if statusOk {
		switch statusErr.GRPCStatus().Code() {
		case codes.NotFound:
			http.Error(w, statusErr.GRPCStatus().Message(), http.StatusNotFound)
			return
		}
	}

	switch {
	case errors.Is(err, errors.ErrEgressNotFound):
		http.Error(w, "unknown egress", http.StatusNotFound)
		return
	case err == nil:
		// break
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	switch app {
	case gstPipelineDotFile:
		dotResp, ok := grpcResp.Response.(*ipc.GetDebugInfoResponse_GstPipelineDot)
		if !ok {
			http.Error(w, "wrong response type", http.StatusInternalServerError)
			return
		}
		dotStr := dotResp.GstPipelineDot.DotFile
		_, err := w.Write([]byte(dotStr))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case pprof:
		pprofResp, ok := grpcResp.Response.(*ipc.GetDebugInfoResponse_Pprof)
		if !ok {
			http.Error(w, "wrong response type", http.StatusInternalServerError)
			return
		}

		w.Header().Add("Content-Type", "application/octet-stream")

		b := pprofResp.Pprof.PprofFile
		_, err := w.Write(b)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}
