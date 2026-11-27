package stats

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// A linked library that registers a labelless gauge on the default registry
// must not reach the service-side merge; only livekit_egress_ families leave
// the handler.
func TestFilterHandlerFamilies_DropsForeignFamilies(t *testing.T) {
	reg := prometheus.NewRegistry()
	egressGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "livekit", Subsystem: "egress", Name: "test_queue_depth",
	})
	foreignGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "cloud", Subsystem: "changepub", Name: "tx_buffers_in_flight",
	})
	foreignCounter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "go_sql_wait_count_total",
	}, []string{"db_name"})
	reg.MustRegister(egressGauge, foreignGauge, foreignCounter)
	egressGauge.Set(3)
	foreignGauge.Set(1)
	foreignCounter.WithLabelValues("project").Inc()

	families, err := reg.Gather()
	require.NoError(t, err)
	require.Len(t, families, 3)

	kept := FilterHandlerFamilies(families)
	require.Len(t, kept, 1)
	require.Equal(t, "livekit_egress_test_queue_depth", kept[0].GetName())
	require.Equal(t, 3.0, kept[0].Metric[0].Gauge.GetValue())
}

// The finalization histogram and the reconnect counter carry the
// livekit_egress_ prefix and promlint names, so they survive the handler
// export filter and reach the service-side merge with their labels intact.
func TestHandlerMonitor_ExportsFinalizationAndReconnects(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newHandlerMonitor("node", "cluster", reg)

	m.ObserveFinalization("room_composite", "file+stream", 12*time.Second)
	m.IncStreamReconnects("room_composite")
	m.IncStreamReconnects("room_composite")

	families, err := reg.Gather()
	require.NoError(t, err)
	kept := FilterHandlerFamilies(families)

	byName := map[string]*dto.MetricFamily{}
	for _, f := range kept {
		byName[f.GetName()] = f
	}

	hist, ok := byName["livekit_egress_finalization_seconds"]
	require.True(t, ok, "finalization histogram must be exported")
	require.Len(t, hist.Metric, 1)
	require.Equal(t, uint64(1), hist.Metric[0].Histogram.GetSampleCount())
	require.Equal(t, 12.0, hist.Metric[0].Histogram.GetSampleSum())
	require.Equal(t, map[string]string{
		"node_id": "node", "cluster_id": "cluster",
		"type": "room_composite", "outputs": "file+stream",
	}, labelMap(hist.Metric[0]))

	counter, ok := byName["livekit_egress_stream_reconnects_total"]
	require.True(t, ok, "reconnect counter must be exported")
	require.Len(t, counter.Metric, 1)
	require.Equal(t, 2.0, counter.Metric[0].Counter.GetValue())
	require.Equal(t, map[string]string{
		"node_id": "node", "cluster_id": "cluster", "type": "room_composite",
	}, labelMap(counter.Metric[0]))
}

func labelMap(m *dto.Metric) map[string]string {
	out := make(map[string]string, len(m.Label))
	for _, l := range m.Label {
		out[l.GetName()] = l.GetValue()
	}
	return out
}
