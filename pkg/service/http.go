package service

import (
	"net/http"
	"strings"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
)

const (
	gstPipelineDotFile = "gst_pipeline"
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
	default:
		http.Error(w, "unkown application", http.StatusNotFound)
		return
	}

	grpcResp, err := p.processManager.sendGrpcDebugRequest(egressId, grpcReq)
	switch {
	case errors.Is(err, errors.ErrEgressNotFound):
		http.Error(w, "unkown egress", http.StatusNotFound)
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
		}
	}
}
