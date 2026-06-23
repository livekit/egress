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
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
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

	mu               deadlock.Mutex
	endedAccumulator []*dto.MetricFamily
	// Drained on every scrape, so per-egress final snapshots show up once.
	pendingMetrics []*dto.MetricFamily
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

// gatherHandlerMetrics holds s.mu for the whole scrape — live gather included —
// so a handler can't be promoted into the accumulator in between reading its
// live value and reading the accumulator, which would count it twice.
func (s *MetricsService) gatherHandlerMetrics() ([]*dto.MetricFamily, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	collected := make([]*dto.MetricFamily, 0)
	for _, g := range s.pm.GetGatherers() {
		f, err := g.Gather()
		if err != nil {
			logger.Warnw("failed to gather metrics from handler", err)
			continue
		}
		collected = append(collected, f...)
	}
	collected = append(collected, s.endedAccumulator...)
	collected = append(collected, s.pendingMetrics...)
	s.pendingMetrics = nil

	merged, err := mergeFamilies(collected)
	if err != nil {
		logger.Warnw("metric merge error", err)
	}
	return merged, err
}

func (s *MetricsService) StoreProcessEndedMetrics(egressID string, metrics string) error {
	m, err := deserializeMetrics(egressID, metrics)
	if err != nil {
		return err
	}

	accumulable, pending := splitForAccumulator(m)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.pm.StoreAccumulatableMetrics(egressID, accumulable)
	s.pendingMetrics = append(s.pendingMetrics, pending...)
	s.mergeInAccumulatorLocked(egressID)
	return nil
}

// MergeInAccumulator folds a finished handler's cached accumulatable tally into
// the accumulator and suppresses its live values.
func (s *MetricsService) MergeInAccumulator(egressID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mergeInAccumulatorLocked(egressID)
}

func (s *MetricsService) mergeInAccumulatorLocked(egressID string) {
	metrics, alreadyFinalized := s.pm.FinalizeMetrics(egressID)
	if alreadyFinalized || len(metrics) == 0 {
		return
	}

	merged, err := mergeFamilies(append(s.endedAccumulator, metrics...))
	if err != nil {
		logger.Errorw("dropping ended-handler metrics due to accumulator merge error",
			err, "egressID", egressID)
		return
	}
	s.endedAccumulator = merged
}

// splitForAccumulator routes gauges and any metric carrying egress_id to
// pending; everything else goes to accumulable. Mixed families are split
// between the two outputs.
func splitForAccumulator(in []*dto.MetricFamily) (accumulable, pending []*dto.MetricFamily) {
	for _, f := range in {
		if f.GetType() == dto.MetricType_GAUGE {
			pending = append(pending, f)
			continue
		}
		var accMetrics, pendMetrics []*dto.Metric
		for _, m := range f.Metric {
			if hasLabel(m.Label, "egress_id") {
				pendMetrics = append(pendMetrics, m)
			} else {
				accMetrics = append(accMetrics, m)
			}
		}
		if len(accMetrics) > 0 {
			accumulable = append(accumulable, familyWithMetrics(f, accMetrics))
		}
		if len(pendMetrics) > 0 {
			pending = append(pending, familyWithMetrics(f, pendMetrics))
		}
	}
	return accumulable, pending
}

// familyWithMetrics builds a fresh family rather than copying src to avoid
// copying the embedded proto MessageState (sync.Mutex).
func familyWithMetrics(src *dto.MetricFamily, metrics []*dto.Metric) *dto.MetricFamily {
	out := &dto.MetricFamily{
		Name:   src.Name,
		Type:   src.Type,
		Help:   src.Help,
		Unit:   src.Unit,
		Metric: metrics,
	}
	return out
}

func hasLabel(labels []*dto.LabelPair, name string) bool {
	for _, l := range labels {
		if l.GetName() == name {
			return true
		}
	}
	return false
}

func deserializeMetrics(egressID string, s string) ([]*dto.MetricFamily, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(s))
	if err != nil {
		logger.Warnw("failed to parse metrics from handler", err, "egress_id", egressID)
		return make([]*dto.MetricFamily, 0), nil // don't return an error, just skip this handler
	}

	return slices.Collect(maps.Values(families)), nil
}

// ErrCannotMergeGauges fires when two gauges share a family name and label
// set — gauges have no meaningful sum, so this is treated as an invariant
// violation. mergeFamilies keeps the first occurrence and returns the error.
var ErrCannotMergeGauges = errors.New("cannot merge gauge metrics with identical labels")

// mergeFamilies aggregates metrics with identical (family, label-set) keys.
// Inputs are read-only; outputs are freshly allocated.
func mergeFamilies(in []*dto.MetricFamily) ([]*dto.MetricFamily, error) {
	type group struct {
		help     string
		unit     string
		typ      dto.MetricType
		byLabels map[string]*dto.Metric
		order    []string
	}

	groups := make(map[string]*group)
	famOrder := make([]string, 0)
	var mergeErrs []error

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
				if err := accumulateInto(g.typ, existing, m); err != nil {
					mergeErrs = append(mergeErrs,
						fmt.Errorf("family %q labels %q: %w", name, key, err))
				}
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
	return out, errors.Join(mergeErrs...)
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
		// Drop quantiles: not addable across handlers.
		sc := in.Summary.GetSampleCount()
		ss := in.Summary.GetSampleSum()
		out.Summary = &dto.Summary{
			SampleCount: &sc,
			SampleSum:   &ss,
		}
	}
	return out
}

func accumulateInto(t dto.MetricType, dst, src *dto.Metric) error {
	switch t {
	case dto.MetricType_COUNTER:
		if dst.Counter == nil || src.Counter == nil {
			return nil
		}
		v := dst.Counter.GetValue() + src.Counter.GetValue()
		dst.Counter.Value = &v
	case dto.MetricType_GAUGE:
		return ErrCannotMergeGauges
	case dto.MetricType_UNTYPED:
		if dst.Untyped == nil || src.Untyped == nil {
			return nil
		}
		v := dst.Untyped.GetValue() + src.Untyped.GetValue()
		dst.Untyped.Value = &v
	case dto.MetricType_HISTOGRAM:
		if dst.Histogram == nil || src.Histogram == nil {
			return nil
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
			return nil
		}
		sc := dst.Summary.GetSampleCount() + src.Summary.GetSampleCount()
		ss := dst.Summary.GetSampleSum() + src.Summary.GetSampleSum()
		dst.Summary.SampleCount = &sc
		dst.Summary.SampleSum = &ss
	}
	return nil
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
