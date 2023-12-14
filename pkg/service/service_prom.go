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
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
)

func (s *Service) CreateGatherer() prometheus.Gatherer {
	return prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		_, span := tracer.Start(context.Background(), "Service.GathererOfHandlerMetrics")
		defer span.End()

		s.mu.RLock()
		defer s.mu.RUnlock()

		gatherers := prometheus.Gatherers{}
		// Include the default repo
		gatherers = append(gatherers, prometheus.DefaultGatherer)
		// add all the active handlers as sources
		for _, v := range s.activeHandlers {
			gatherers = append(gatherers, v)
		}
		return gatherers.Gather()
	})
}

func (s *Service) PromHandler() http.Handler {
	return promhttp.InstrumentMetricHandler(
		prometheus.DefaultRegisterer, promhttp.HandlerFor(s.CreateGatherer(), promhttp.HandlerOpts{}),
	)
}

func (s *Service) promIsIdle() float64 {
	if s.GetRequestCount() == 0 {
		return 1
	}
	return 0
}

func (s *Service) promCanAcceptRequest() float64 {
	if s.CanAcceptRequest(&rpc.StartEgressRequest{
		Request: &rpc.StartEgressRequest_RoomComposite{
			RoomComposite: &livekit.RoomCompositeEgressRequest{},
		},
	}) {
		return 1
	}
	return 0
}

func (s *Service) promProcUpdate(pUsage map[int]float64) map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	eUsage := make(map[string]float64)
	for _, h := range s.activeHandlers {
		if cmd := h.cmd; cmd != nil {
			if process := cmd.Process; process != nil {
				if usage, ok := pUsage[process.Pid]; ok {
					eUsage[h.req.EgressId] = usage
					h.updateCPU(usage)
				}
			}
		}
	}

	return eUsage
}
