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

	"github.com/linkdata/deadlock"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"golang.org/x/exp/maps"

	"github.com/livekit/protocol/logger"
	"go.opentelemetry.io/otel"
)

type MetricsService struct {
	pm *ProcessManager

	mu             deadlock.Mutex
	pendingMetrics []*dto.MetricFamily
}

var (
	tracer = otel.Tracer("github.com/livekit/egress/pkg/service")
)

func NewMetricsService(pm *ProcessManager) *MetricsService {
	prometheus.Unregister(collectors.NewGoCollector())
	prometheus.MustRegister(collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsAll)))

	return &MetricsService{
		pm: pm,
	}
}

func (s *MetricsService) PromHandler() http.Handler {
	return promhttp.InstrumentMetricHandler(
		prometheus.DefaultRegisterer, promhttp.HandlerFor(s.CreateGatherer(), promhttp.HandlerOpts{}),
	)
}

func (s *MetricsService) CreateGatherer() prometheus.Gatherer {
	return prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		_, span := tracer.Start(context.Background(), "Service.GathererOfHandlerMetrics")
		defer span.End()

		gatherers := prometheus.Gatherers{}
		// Include the default repo
		gatherers = append(gatherers, prometheus.DefaultGatherer)
		// Include Process ended ms
		gatherers = append(gatherers, prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
			s.mu.Lock()
			m := s.pendingMetrics
			s.pendingMetrics = nil
			s.mu.Unlock()
			return m, nil
		}))

		gatherers = append(gatherers, s.pm.GetGatherers()...)

		return gatherers.Gather()
	})
}

func (s *MetricsService) StoreProcessEndedMetrics(egressID string, metrics string) error {
	m, err := deserializeMetrics(egressID, metrics)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.pendingMetrics = append(s.pendingMetrics, m...)
	s.mu.Unlock()

	return nil
}

func deserializeMetrics(egressID string, s string) ([]*dto.MetricFamily, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(s))
	if err != nil {
		logger.Warnw("failed to parse ms from handler", err, "egress_id", egressID)
		return make([]*dto.MetricFamily, 0), nil // don't return an error, just skip this handler
	}

	// Add an egress_id label to every metric all the families, if it doesn't already have one
	applyDefaultLabel(families, egressID)

	return maps.Values(families), nil
}

func applyDefaultLabel(families map[string]*dto.MetricFamily, egressID string) {
	egressIDLabel := "egress_id"
	egressLabelPair := &dto.LabelPair{
		Name:  &egressIDLabel,
		Value: &egressID,
	}
	for _, family := range families {
		for _, metric := range family.Metric {
			if metric.Label == nil {
				metric.Label = make([]*dto.LabelPair, 0)
			}
			found := false
			for _, label := range metric.Label {
				if label.GetName() == "egress_id" {
					found = true
					break
				}
			}
			if !found {
				metric.Label = append(metric.Label, egressLabelPair)
			}
		}
	}
}
