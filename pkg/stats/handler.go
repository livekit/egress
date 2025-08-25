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

package stats

import (
	"github.com/prometheus/client_golang/prometheus"
)

type HandlerMonitor struct {
	uploadsCounter      *prometheus.CounterVec
	uploadsResponseTime *prometheus.HistogramVec
	backupCounter       *prometheus.CounterVec
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

	m.backupCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "backup_storage_writes",
		Help:        "number of writes to backup storage location by output type",
		ConstLabels: constantLabels,
	}, []string{"output_type"})

	prometheus.MustRegister(m.uploadsCounter, m.uploadsResponseTime, m.backupCounter)

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

func (m *HandlerMonitor) IncBackupStorageWrites(outputType string) {
	m.backupCounter.With(prometheus.Labels{"output_type": outputType}).Add(1)
}

func (m *HandlerMonitor) RegisterSegmentsChannelSizeGauge(nodeId string, clusterId string, egressId string, channelSizeFunction func() float64) {
	segmentsUploadsGauge := prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Namespace:   "livekit",
			Subsystem:   "egress",
			Name:        "segments_uploads_channel_size",
			Help:        "number of segment uploads pending in channel",
			ConstLabels: prometheus.Labels{"node_id": nodeId, "cluster_id": clusterId, "egress_id": egressId},
		}, channelSizeFunction)
	prometheus.MustRegister(segmentsUploadsGauge)
}

func (m *HandlerMonitor) RegisterPlaylistChannelSizeGauge(nodeId string, clusterId string, egressId string, channelSizeFunction func() float64) {
	playlistUploadsGauge := prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Namespace:   "livekit",
			Subsystem:   "egress",
			Name:        "playlist_uploads_channel_size",
			Help:        "number of playlist updates pending in channel",
			ConstLabels: prometheus.Labels{"node_id": nodeId, "cluster_id": clusterId, "egress_id": egressId},
		}, channelSizeFunction)
	prometheus.MustRegister(playlistUploadsGauge)
}
