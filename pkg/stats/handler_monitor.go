package stats

import (
	"github.com/prometheus/client_golang/prometheus"
)

type HandlerMonitor struct {
	uploadsCounter      *prometheus.CounterVec
	uploadsResponseTime *prometheus.HistogramVec
}

func NewHandlerMonitor(nodeId string, clusterId string, egressId string) *HandlerMonitor {
	m := &HandlerMonitor{}

	constantLabels := prometheus.Labels{"node_id": nodeId, "cluster_id": clusterId, "egress_id": egressId}

	m.uploadsCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "pipeline_uploads",
		Help:        "Number of uploads per pipeline with type and status labels",
		ConstLabels: constantLabels,
	}, []string{"type", "status"}) // type: file, manifest, segment, liveplaylist, playlist; status: success,failure

	m.uploadsResponseTime = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "pipline_upload_response_time_ms",
		Help:        "A histogram of latencies for upload requests in milliseconds.",
		Buckets:     []float64{10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 15000, 20000, 30000},
		ConstLabels: constantLabels,
	}, []string{"type", "status"})
	prometheus.MustRegister(m.uploadsCounter, m.uploadsResponseTime)

	return m
}

func (m *HandlerMonitor) IncUploadCountSuccess(uploadType string, elapsed float64) {
	labels := prometheus.Labels{"type": uploadType, "status": "success"}
	m.uploadsCounter.With(labels).Add(1)
	m.uploadsResponseTime.With(labels).Observe(elapsed)
}

func (m *HandlerMonitor) IncUploadCountFailure(uploadType string, elapsed float64) {
	labels := prometheus.Labels{"type": uploadType, "status": "failure"}
	m.uploadsCounter.With(labels).Add(1)
	m.uploadsResponseTime.With(labels).Observe(elapsed)
}
