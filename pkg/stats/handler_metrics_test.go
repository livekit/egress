package stats

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
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
