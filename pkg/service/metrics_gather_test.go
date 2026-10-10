package service

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/stats"
)

type gatherersProcessManager struct {
	ProcessManager
	gatherers []prometheus.Gatherer
}

func (pm gatherersProcessManager) GetGatherers() []prometheus.Gatherer { return pm.gatherers }

// newHandlerRegistry models one handler process: an egress counter that sums
// across handlers, an egress gauge unique per egress_id, and the foreign
// labelless gauge a linked library registers in every process.
func newHandlerRegistry(t *testing.T, egressID string, uploads float64, queueDepth float64) *prometheus.Registry {
	t.Helper()
	reg := prometheus.NewRegistry()
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "livekit", Subsystem: "egress", Name: "test_uploads",
		ConstLabels: prometheus.Labels{"node_id": "NE_test"},
	}, []string{"type"})
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "livekit", Subsystem: "egress", Name: "test_queue_depth",
		ConstLabels: prometheus.Labels{"node_id": "NE_test", "egress_id": egressID},
	})
	foreign := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "cloud", Subsystem: "foreignlib", Name: "buffers_in_flight",
	})
	reg.MustRegister(counter, gauge, foreign)
	counter.WithLabelValues("file").Add(uploads)
	gauge.Set(queueDepth)
	foreign.Set(1)
	return reg
}

// handlerGatherer reproduces Handler.GenerateMetrics and Process.Gather: gather
// the handler registry, optionally apply the handler-side filter, render to
// text, and parse it back on the service side.
func handlerGatherer(t *testing.T, reg *prometheus.Registry, egressID string, filter bool) prometheus.Gatherer {
	t.Helper()
	return prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		families, err := reg.Gather()
		if err != nil {
			return nil, err
		}
		if filter {
			families = stats.FilterHandlerFamilies(families)
		}
		var sb strings.Builder
		for _, f := range families {
			if _, err := expfmt.MetricFamilyToText(&sb, f); err != nil {
				return nil, err
			}
		}
		return deserializeMetrics(egressID, sb.String())
	})
}

func familyByName(families []*dto.MetricFamily, name string) *dto.MetricFamily {
	for _, f := range families {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

// The service registry and every handler carry the same labelless foreign
// gauge. With the handler-side filter the scrape gathers cleanly: egress
// counters sum, per-egress gauges stay distinct, and the foreign gauge appears
// once, from the service. Without the filter the same setup fails the gather.
func TestCreateGatherer_ForeignGaugeInServiceAndHandlers(t *testing.T) {
	serviceForeign := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "cloud", Subsystem: "foreignlib", Name: "buffers_in_flight",
	})
	require.NoError(t, prometheus.DefaultRegisterer.Register(serviceForeign))
	t.Cleanup(func() { prometheus.DefaultRegisterer.Unregister(serviceForeign) })
	serviceForeign.Set(7)

	regA := newHandlerRegistry(t, "EG_a", 3, 2)
	regB := newHandlerRegistry(t, "EG_b", 5, 4)

	t.Run("filtered handlers", func(t *testing.T) {
		s := &MetricsService{pm: gatherersProcessManager{gatherers: []prometheus.Gatherer{
			handlerGatherer(t, regA, "EG_a", true),
			handlerGatherer(t, regB, "EG_b", true),
		}}}

		families, err := s.CreateGatherer().Gather()
		require.NoError(t, err)

		uploads := familyByName(families, "livekit_egress_test_uploads")
		require.NotNil(t, uploads)
		require.Len(t, uploads.Metric, 1)
		require.Equal(t, 8.0, uploads.Metric[0].Counter.GetValue(), "handler counters sum")

		depth := familyByName(families, "livekit_egress_test_queue_depth")
		require.NotNil(t, depth)
		require.Len(t, depth.Metric, 2, "per-egress gauges stay distinct")
		require.Equal(t, 2.0, findMetric(t, depth, "node_id", "NE_test", "egress_id", "EG_a").Gauge.GetValue())
		require.Equal(t, 4.0, findMetric(t, depth, "node_id", "NE_test", "egress_id", "EG_b").Gauge.GetValue())

		foreign := familyByName(families, "cloud_foreignlib_buffers_in_flight")
		require.NotNil(t, foreign)
		require.Len(t, foreign.Metric, 1, "foreign gauge comes from the service registry only")
		require.Equal(t, 7.0, foreign.Metric[0].Gauge.GetValue())
	})

	t.Run("unfiltered handlers fail the gather", func(t *testing.T) {
		s := &MetricsService{pm: gatherersProcessManager{gatherers: []prometheus.Gatherer{
			handlerGatherer(t, regA, "EG_a", false),
			handlerGatherer(t, regB, "EG_b", false),
		}}}

		_, err := s.CreateGatherer().Gather()
		require.Error(t, err)
		// prometheus.MultiError has no Unwrap, so match on the messages.
		require.Contains(t, err.Error(), ErrCannotMergeGauges.Error())
		require.Contains(t, err.Error(), "was collected before with the same name and label values")
	})
}
