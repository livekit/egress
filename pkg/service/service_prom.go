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
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"golang.org/x/exp/maps"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
)

func (s *Service) CreateGatherer() prometheus.Gatherer {
	return prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		_, span := tracer.Start(context.Background(), "Service.GathererOfHandlerMetrics")
		defer span.End()

		gatherers := prometheus.Gatherers{}
		// Include the default repo
		gatherers = append(gatherers, prometheus.DefaultGatherer)
		// Include process ended metrics
		gatherers = append(gatherers, prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
			s.mu.Lock()

			m := s.pendingMetrics
			s.pendingMetrics = nil

			s.mu.Unlock()

			return m, nil
		}))

		s.mu.RLock()
		// add all the active handlers as sources
		for _, v := range s.activeHandlers {
			gatherers = append(gatherers, v)
		}
		s.mu.RUnlock()

		return gatherers.Gather()
	})
}

func (s *Service) PromHandler() http.Handler {
	return promhttp.InstrumentMetricHandler(
		prometheus.DefaultRegisterer, promhttp.HandlerFor(s.CreateGatherer(), promhttp.HandlerOpts{}),
	)
}

func (s *Service) promIsIdle() float64 {
	if !s.shutdown.IsBroken() && s.GetRequestCount() == 0 {
		return 1
	}
	return 0
}

func (s *Service) promCanAcceptRequest() float64 {
	if s.shutdown.IsBroken() {
		return 0
	}
	if s.CanAcceptRequest(&rpc.StartEgressRequest{
		Request: &rpc.StartEgressRequest_RoomComposite{
			RoomComposite: &livekit.RoomCompositeEgressRequest{},
		},
	}) {
		return 1
	}
	return 0
}

func (s *Service) promIsDisabled() float64 {
	if s.shutdown.IsBroken() {
		return 1
	}
	return 0
}

func (s *Service) storeProcessEndedMetrics(egressID string, metrics string) error {
	m, err := deserializeMetrics(egressID, metrics)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.pendingMetrics = append(s.pendingMetrics, m...)

	return nil
}

func deserializeMetrics(egressID string, s string) ([]*dto.MetricFamily, error) {
	parser := &expfmt.TextParser{}
	families, err := parser.TextToMetricFamilies(strings.NewReader(s))
	if err != nil {
		logger.Warnw("failed to parse metrics from handler", err, "egress_id", egressID)
		return make([]*dto.MetricFamily, 0), nil // don't return an error, just skip this handler
	}

	// Add an egress_id label to every metric all the families, if it doesn't already have one
	applyDefaultLabel(egressID, families)

	return maps.Values(families), nil
}
