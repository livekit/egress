package service

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/egress/pkg/pprof"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	gstPipelineDotFileApp = "gst_pipeline"
	pprofApp              = "pprof"
)

type gstDotFileDebugHandler struct {
	pm *ProcessManager
}

// URL path format is "/<application>/<egress_id>/<optional_other_params>"
func (p *gstDotFileDebugHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	pathElements := strings.Split(r.URL.Path, "/")
	if len(pathElements) < 3 {
		http.Error(w, "malformed url", http.StatusNotFound)
		return
	}

	egressId := pathElements[2]

	grpcReq := &ipc.GetDebugInfoRequest{
		Request: &ipc.GetDebugInfoRequest_GstPipelineDot{
			GstPipelineDot: &ipc.GstPipelineDebugDotRequest{},
		},
	}

	grpcResp, err, code := sendHandlerRpcRequest(p.pm, egressId, grpcReq)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}

	dotResp, ok := grpcResp.Response.(*ipc.GetDebugInfoResponse_GstPipelineDot)
	if !ok {
		http.Error(w, "wrong response type", http.StatusInternalServerError)
		return
	}
	dotStr := dotResp.GstPipelineDot.DotFile
	_, err = w.Write([]byte(dotStr))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

type pprofDebugHandler struct {
	pm *ProcessManager
}

// URL path format is "/<application>/<egress_id>/<profile_name>" or "/<application>/<profile_name>" to profile the service
func (p *pprofDebugHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var err error
	var b []byte

	timeout, _ := strconv.Atoi(r.URL.Query().Get("timeout"))
	debug, _ := strconv.Atoi(r.URL.Query().Get("debug"))

	pathElements := strings.Split(r.URL.Path, "/")
	switch len(pathElements) {
	case 3:
		// profile main service
		b, err = pprof.GetProfileData(context.Background(), pathElements[2], timeout, debug)
		switch err {
		case nil:
			// break
		case pprof.ErrProfileNotFound:
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case 4:
		egressId := pathElements[2]
		grpcReq := &ipc.GetDebugInfoRequest{
			Request: &ipc.GetDebugInfoRequest_Pprof{
				Pprof: &ipc.PprofRequest{
					ProfileName: pathElements[3],
					Timeout:     int32(timeout),
					Debug:       int32(debug),
				},
			},
		}

		grpcResp, err, code := sendHandlerRpcRequest(p.pm, egressId, grpcReq)
		if err != nil {
			http.Error(w, err.Error(), code)
			return
		}

		pprofResp, ok := grpcResp.Response.(*ipc.GetDebugInfoResponse_Pprof)
		if !ok {
			http.Error(w, "wrong response type", http.StatusInternalServerError)
			return
		}

		b = pprofResp.Pprof.PprofFile
	default:
		http.Error(w, "malformed url", http.StatusNotFound)
		return
	}

	w.Header().Add("Content-Type", "application/octet-stream")

	_, err = w.Write(b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func sendHandlerRpcRequest(pm *ProcessManager, egressId string, req *ipc.GetDebugInfoRequest) (resp *ipc.GetDebugInfoResponse, err error, statusCode int) {
	grpcResp, err := pm.sendGrpcDebugRequest(egressId, req)
	statusErr, statusOk := err.(interface {
		GRPCStatus() *status.Status
	})

	if statusOk {
		switch statusErr.GRPCStatus().Code() {
		case codes.NotFound:
			return nil, err, http.StatusNotFound
		}
	}

	switch {
	case errors.Is(err, errors.ErrEgressNotFound):
		return nil, errors.ErrEgressNotFound, http.StatusNotFound
	case err == nil:
		// break
	default:
		return nil, err, http.StatusInternalServerError
	}

	return grpcResp, nil, http.StatusOK
}
