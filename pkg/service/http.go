package service

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/egress/pkg/pprof"
	"github.com/livekit/protocol/logger"
)

const (
	gstPipelineDotFileApp = "gst_pipeline"
	pprofApp              = "pprof"
)

func (s *Service) StartDebugHandlers() {
	if s.conf.DebugHandlerPort == 0 {
		logger.Debugw("debug handler disabled")
	}

	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/%s/", gstPipelineDotFileApp), s.handleGstPipelineDotFile)
	mux.HandleFunc(fmt.Sprintf("/%s/", pprofApp), s.handlePProf)

	go func() {
		addr := fmt.Sprintf(":%d", s.conf.DebugHandlerPort)

		logger.Debugw(fmt.Sprintf("starting debug handler on address %s", addr))
		err := http.ListenAndServe(addr, mux)
		logger.Infow("debug server failed", "error", err)
	}()
}

// URL path format is "/<application>/<egress_id>/<optional_other_params>"
func (s *Service) handleGstPipelineDotFile(w http.ResponseWriter, r *http.Request) {
	pathElements := strings.Split(r.URL.Path, "/")
	if len(pathElements) < 3 {
		http.Error(w, "malformed url", http.StatusNotFound)
		return
	}

	egressID := pathElements[2]
	c, err := s.manager.getGRPCClient(egressID)
	if err != nil {
		http.Error(w, "handler not found", http.StatusNotFound)
		return
	}

	res, err := c.GetPipelineDot(context.Background(), &ipc.GstPipelineDebugDotRequest{})
	if err == nil {
		_, err = w.Write([]byte(res.DotFile))
	}
	if err != nil {
		http.Error(w, err.Error(), getErrorCode(err))
		return
	}
}

// URL path format is "/<application>/<egress_id>/<profile_name>" or "/<application>/<profile_name>" to profile the service
func (s *Service) handlePProf(w http.ResponseWriter, r *http.Request) {
	var err error
	var b []byte

	timeout, _ := strconv.Atoi(r.URL.Query().Get("timeout"))
	debug, _ := strconv.Atoi(r.URL.Query().Get("debug"))

	pathElements := strings.Split(r.URL.Path, "/")
	switch len(pathElements) {
	case 3:
		// profile main service
		b, err = pprof.GetProfileData(context.Background(), pathElements[2], timeout, debug)

	case 4:
		egressID := pathElements[2]
		c, err := s.manager.getGRPCClient(egressID)
		if err != nil {
			http.Error(w, "handler not found", http.StatusNotFound)
			return
		}

		res, err := c.GetPProf(context.Background(), &ipc.PProfRequest{
			ProfileName: pathElements[3],
			Timeout:     int32(timeout),
			Debug:       int32(debug),
		})
		if err == nil {
			b = res.PprofFile
		}
	default:
		http.Error(w, "malformed url", http.StatusNotFound)
		return
	}

	if err == nil {
		w.Header().Add("Content-Type", "application/octet-stream")
		_, err = w.Write(b)
	}
	if err != nil {
		http.Error(w, err.Error(), getErrorCode(err))
		return
	}
}

func getErrorCode(err error) int {
	statusErr, statusOk := err.(interface {
		GRPCStatus() *status.Status
	})

	if statusOk {
		switch statusErr.GRPCStatus().Code() {
		case codes.NotFound:
			return http.StatusNotFound
		}
	}

	switch {
	case errors.Is(err, pprof.ErrProfileNotFound), errors.Is(err, errors.ErrEgressNotFound):
		return http.StatusNotFound
	case err == nil:
		return http.StatusOK
	default:
		return http.StatusInternalServerError
	}
}
