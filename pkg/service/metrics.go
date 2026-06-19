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
	"sort"
	"strings"

	"github.com/linkdata/deadlock"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/livekit/protocol/logger"
	"go.opentelemetry.io/otel"
)

type MetricsService struct {
	pm ProcessManager

	mu deadlock.Mutex
	// Running totals of counter/histogram metrics from finished handlers,
	// merged by (family name, label set). Bounded by label cardinality, not
	// by handler history.
	endedAccumulator []*dto.MetricFamily
}

var (
	tracer = otel.Tracer("github.com/livekit/egress/pkg/service")
)

func NewMetricsService(pm ProcessManager) *MetricsService {
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

		gatherers := prometheus.Gatherers{
			prometheus.DefaultGatherer,
			prometheus.GathererFunc(s.gatherHandlerMetrics),
		}
		return gatherers.Gather()
	})
}

// gatherHandlerMetrics produces a single set of merged metric families across
// all live handlers and the accumulator of finished-handler totals.
func (s *MetricsService) gatherHandlerMetrics() ([]*dto.MetricFamily, error) {
	live := s.pm.GetGatherers()

	collected := make([]*dto.MetricFamily, 0)
	for _, g := range live {
		f, err := g.Gather()
		if err != nil {
			logger.Warnw("failed to gather metrics from handler", err)
			continue
		}
		collected = append(collected, f...)
	}

	s.mu.Lock()
	collected = append(collected, s.endedAccumulator...)
	s.mu.Unlock()

	return mergeFamilies(collected), nil
}

func (s *MetricsService) StoreProcessEndedMetrics(egressID string, metrics string) error {
	m, err := parseMetrics(egressID, metrics)
	if err != nil {
		return err
	}

	// Stop including this handler's live metrics in gather output before we
	// fold its final tally into the accumulator, so we don't briefly
	// double-count if the subprocess is still alive and answering IPC.
	s.pm.MarkMetricsFinalized(egressID)

	s.mu.Lock()
	s.endedAccumulator = mergeFamilies(append(s.endedAccumulator, m...))
	s.mu.Unlock()

	return nil
}

func parseMetrics(egressID string, s string) ([]*dto.MetricFamily, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(s))
	if err != nil {
		logger.Warnw("failed to parse metrics from handler", err, "egress_id", egressID)
		return make([]*dto.MetricFamily, 0), nil // don't return an error, just skip this handler
	}

	out := make([]*dto.MetricFamily, 0, len(families))
	for _, f := range families {
		out = append(out, f)
	}
	return out, nil
}

// mergeFamilies groups input families by name and, within each family, groups
// metrics by their full label set. Counters, gauges, histograms, summaries and
// untyped metrics with identical (family, label-set) keys are aggregated.
// Inputs are treated as read-only; outputs are freshly allocated, so callers
// can safely re-merge a returned slice without aliasing concerns.
func mergeFamilies(in []*dto.MetricFamily) []*dto.MetricFamily {
	type group struct {
		help     string
		unit     string
		typ      dto.MetricType
		byLabels map[string]*dto.Metric
		order    []string
	}

	groups := make(map[string]*group)
	famOrder := make([]string, 0)

	for _, f := range in {
		name := f.GetName()
		g, ok := groups[name]
		if !ok {
			g = &group{
				help:     f.GetHelp(),
				unit:     f.GetUnit(),
				typ:      f.GetType(),
				byLabels: make(map[string]*dto.Metric),
			}
			groups[name] = g
			famOrder = append(famOrder, name)
		}
		for _, m := range f.Metric {
			key := labelSetKey(m.Label)
			if existing, found := g.byLabels[key]; found {
				accumulateInto(g.typ, existing, m)
			} else {
				g.byLabels[key] = cloneMetric(m)
				g.order = append(g.order, key)
			}
		}
	}

	sort.Strings(famOrder)
	out := make([]*dto.MetricFamily, 0, len(famOrder))
	for _, name := range famOrder {
		g := groups[name]
		metrics := make([]*dto.Metric, 0, len(g.order))
		for _, k := range g.order {
			metrics = append(metrics, g.byLabels[k])
		}
		nameCopy := name
		family := &dto.MetricFamily{
			Name:   &nameCopy,
			Type:   metricTypePtr(g.typ),
			Metric: metrics,
		}
		if g.help != "" {
			h := g.help
			family.Help = &h
		}
		if g.unit != "" {
			u := g.unit
			family.Unit = &u
		}
		out = append(out, family)
	}
	return out
}

func cloneMetric(in *dto.Metric) *dto.Metric {
	out := &dto.Metric{
		Label: in.Label, // LabelPair values are immutable; sharing is safe
	}
	if in.Counter != nil {
		v := in.Counter.GetValue()
		out.Counter = &dto.Counter{Value: &v}
	}
	if in.Gauge != nil {
		v := in.Gauge.GetValue()
		out.Gauge = &dto.Gauge{Value: &v}
	}
	if in.Untyped != nil {
		v := in.Untyped.GetValue()
		out.Untyped = &dto.Untyped{Value: &v}
	}
	if in.Histogram != nil {
		sc := in.Histogram.GetSampleCount()
		ss := in.Histogram.GetSampleSum()
		h := &dto.Histogram{
			SampleCount: &sc,
			SampleSum:   &ss,
		}
		for _, b := range in.Histogram.Bucket {
			cc := b.GetCumulativeCount()
			ub := b.GetUpperBound()
			h.Bucket = append(h.Bucket, &dto.Bucket{
				CumulativeCount: &cc,
				UpperBound:      &ub,
			})
		}
		out.Histogram = h
	}
	if in.Summary != nil {
		// Quantiles are not meaningfully addable across handlers; we keep
		// only count and sum so rate(_count) and rate(_sum) stay correct.
		sc := in.Summary.GetSampleCount()
		ss := in.Summary.GetSampleSum()
		out.Summary = &dto.Summary{
			SampleCount: &sc,
			SampleSum:   &ss,
		}
	}
	return out
}

func accumulateInto(t dto.MetricType, dst, src *dto.Metric) {
	switch t {
	case dto.MetricType_COUNTER:
		if dst.Counter == nil || src.Counter == nil {
			return
		}
		v := dst.Counter.GetValue() + src.Counter.GetValue()
		dst.Counter.Value = &v
	case dto.MetricType_GAUGE:
		if dst.Gauge == nil || src.Gauge == nil {
			return
		}
		v := dst.Gauge.GetValue() + src.Gauge.GetValue()
		dst.Gauge.Value = &v
	case dto.MetricType_UNTYPED:
		if dst.Untyped == nil || src.Untyped == nil {
			return
		}
		v := dst.Untyped.GetValue() + src.Untyped.GetValue()
		dst.Untyped.Value = &v
	case dto.MetricType_HISTOGRAM:
		if dst.Histogram == nil || src.Histogram == nil {
			return
		}
		sc := dst.Histogram.GetSampleCount() + src.Histogram.GetSampleCount()
		ss := dst.Histogram.GetSampleSum() + src.Histogram.GetSampleSum()
		dst.Histogram.SampleCount = &sc
		dst.Histogram.SampleSum = &ss
		srcByBound := make(map[float64]uint64, len(src.Histogram.Bucket))
		for _, b := range src.Histogram.Bucket {
			srcByBound[b.GetUpperBound()] = b.GetCumulativeCount()
		}
		for _, b := range dst.Histogram.Bucket {
			if c, ok := srcByBound[b.GetUpperBound()]; ok {
				v := b.GetCumulativeCount() + c
				b.CumulativeCount = &v
			}
		}
	case dto.MetricType_SUMMARY:
		if dst.Summary == nil || src.Summary == nil {
			return
		}
		sc := dst.Summary.GetSampleCount() + src.Summary.GetSampleCount()
		ss := dst.Summary.GetSampleSum() + src.Summary.GetSampleSum()
		dst.Summary.SampleCount = &sc
		dst.Summary.SampleSum = &ss
	}
}

func labelSetKey(labels []*dto.LabelPair) string {
	if len(labels) == 0 {
		return ""
	}
	pairs := make([]string, len(labels))
	for i, l := range labels {
		pairs[i] = l.GetName() + "\x00" + l.GetValue()
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "\x01")
}

func metricTypePtr(t dto.MetricType) *dto.MetricType {
	v := t
	return &v
}
