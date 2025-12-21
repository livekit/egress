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

package handler

import (
	"context"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/ipc"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/pprof"
	"github.com/livekit/psrpc"
)

func (h *Handler) GetPipelineDot(ctx context.Context, _ *ipc.GstPipelineDebugDotRequest) (*ipc.GstPipelineDebugDotResponse, error) {
	_, span := tracer.Start(ctx, "Handler.GetPipelineDot")
	defer span.End()

	<-h.initialized.Watch()
	if h.controller == nil {
		// egress handler is shutting down on error
		return nil, errors.ErrEgressNotFound
	}

	r, err := h.controller.GetGstPipelineDebugDot()
	if err != nil {
		return nil, err
	}

	return &ipc.GstPipelineDebugDotResponse{
		DotFile: r,
	}, nil
}

func (h *Handler) GetPProf(ctx context.Context, req *ipc.PProfRequest) (*ipc.PProfResponse, error) {
	ctx, span := tracer.Start(ctx, "Handler.GetPProf")
	defer span.End()

	<-h.initialized.Watch()
	if h.controller == nil {
		// egress handler is shutting down on error
		return nil, errors.ErrEgressNotFound
	}

	b, err := pprof.GetProfileData(ctx, req.ProfileName, int(req.Timeout), int(req.Debug))
	if err != nil {
		return nil, err
	}

	return &ipc.PProfResponse{
		PprofFile: b,
	}, nil
}

// GetMetrics implement the handler-side gathering of metrics to return over IPC
func (h *Handler) GetMetrics(ctx context.Context, _ *ipc.MetricsRequest) (*ipc.MetricsResponse, error) {
	ctx, span := tracer.Start(ctx, "Handler.GetMetrics")
	defer span.End()

	metricsAsString, err := h.GenerateMetrics(ctx)
	if err != nil {
		return nil, err
	}

	return &ipc.MetricsResponse{
		Metrics: metricsAsString,
	}, nil
}

func (h *Handler) GenerateMetrics(_ context.Context) (string, error) {
	metrics, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return "", err
	}

	metricsAsString, err := renderMetrics(metrics)
	if err != nil {
		return "", err
	}

	return metricsAsString, nil
}

func renderMetrics(metrics []*dto.MetricFamily) (string, error) {
	// Create a StringWriter to render the metrics into text format
	writer := &strings.Builder{}
	totalCnt := 0
	for _, metric := range metrics {
		// Write each metric family to text
		cnt, err := expfmt.MetricFamilyToText(writer, metric)
		if err != nil {
			logger.Errorw("error writing metric family", err)
			return "", err
		}
		totalCnt += cnt
	}

	// Get the rendered metrics as a string from the StringWriter
	return writer.String(), nil
}

func (h *Handler) KillEgress(ctx context.Context, req *ipc.KillEgressRequest) (*emptypb.Empty, error) {
	ctx, span := tracer.Start(ctx, "Handler.KillEgress")
	defer span.End()

	<-h.initialized.Watch()

	if h.controller == nil {
		// failed to start controller
		return &emptypb.Empty{}, nil
	}

	h.controller.SendEOS(ctx, livekit.EndReasonKilled)
	h.controller.Info.SetFailed(psrpc.NewErrorf(psrpc.PermissionDenied, req.Error))

	return &emptypb.Empty{}, nil
}
