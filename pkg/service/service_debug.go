// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/pprof"
	"github.com/livekit/psrpc"
)

const (
	gstPipelineDotFileApp = "gst_pipeline"
	pprofApp              = "pprof"
)

func (s *Service) StartDebugHandlers() {
	if s.conf.DebugHandlerPort == 0 {
		logger.Debugw("debug handler disabled")
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/%s/", gstPipelineDotFileApp), s.handleGstPipelineDotFile)
	mux.HandleFunc(fmt.Sprintf("/%s/", pprofApp), s.handlePProf)

	go func() {
		addr := fmt.Sprintf(":%d", s.conf.DebugHandlerPort)
		logger.Debugw(fmt.Sprintf("starting debug handler on address %s", addr))
		_ = http.ListenAndServe(addr, mux)
	}()
}

func (s *Service) GetGstPipelineDotFile(egressID string) (string, error) {
	c, err := s.getGRPCClient(egressID)
	if err != nil {
		return "", err
	}

	res, err := c.GetPipelineDot(context.Background(), &ipc.GstPipelineDebugDotRequest{})
	if err != nil {
		return "", err
	}
	return res.DotFile, nil
}

// URL path format is "/<application>/<egress_id>/<optional_other_params>"
func (s *Service) handleGstPipelineDotFile(w http.ResponseWriter, r *http.Request) {
	pathElements := strings.Split(r.URL.Path, "/")
	if len(pathElements) < 3 {
		http.Error(w, "malformed url", http.StatusNotFound)
		return
	}

	egressID := pathElements[2]
	dotFile, err := s.GetGstPipelineDotFile(egressID)
	if err != nil {
		http.Error(w, err.Error(), getErrorCode(err))
		return
	}
	_, _ = w.Write([]byte(dotFile))
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
		c, err := s.getGRPCClient(egressID)
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

func (s *Service) getGRPCClient(egressID string) (ipc.EgressHandlerClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	h, ok := s.activeHandlers[egressID]
	if !ok {
		return nil, errors.ErrEgressNotFound
	}
	return h.ipcHandlerClient, nil
}

func getErrorCode(err error) int {
	var e psrpc.Error

	switch {
	case errors.As(err, &e):
		return e.ToHttp()
	case err == nil:
		return http.StatusOK
	default:
		return http.StatusInternalServerError
	}
}
