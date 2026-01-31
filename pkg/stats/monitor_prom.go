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

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
)

func (m *Monitor) initPrometheus() {
	promNodeAvailable := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "available",
		ConstLabels: prometheus.Labels{"node_id": m.nodeID, "cluster_id": m.clusterID},
	}, m.promIsIdle)

	promCanAcceptRequest := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "can_accept_request",
		ConstLabels: prometheus.Labels{"node_id": m.nodeID, "cluster_id": m.clusterID},
	}, m.promCanAcceptRequest)

	promIsDisabled := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "is_disabled",
		ConstLabels: prometheus.Labels{"node_id": m.nodeID, "cluster_id": m.clusterID},
	}, m.promIsDisabled)

	promIsTerminating := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "is_terminating",
		ConstLabels: prometheus.Labels{"node_id": m.nodeID, "cluster_id": m.clusterID},
	}, m.promIsTerminating)

	m.promCPULoad = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "node",
		Name:        "cpu_load",
		ConstLabels: prometheus.Labels{"node_id": m.nodeID, "node_type": "EGRESS", "cluster_id": m.clusterID},
	})

	m.requestGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "egress",
		Name:        "requests",
		ConstLabels: prometheus.Labels{"node_id": m.nodeID, "cluster_id": m.clusterID},
	}, []string{"type"})

	prometheus.MustRegister(promNodeAvailable, promCanAcceptRequest, promIsDisabled, promIsTerminating, m.promCPULoad, m.requestGauge)
}

func (m *Monitor) promIsIdle() float64 {
	if m.svc.IsIdle() {
		return 1
	}
	return 0
}

func (m *Monitor) promCanAcceptRequest() float64 {
	m.mu.Lock()
	_, canAccept := m.canAcceptRequestLocked(&rpc.StartEgressRequest{
		Request: &rpc.StartEgressRequest_Web{Web: &livekit.WebEgressRequest{}},
	}, false)
	m.mu.Unlock()

	if !m.svc.IsDisabled() && canAccept {
		return 1
	}
	return 0
}

func (m *Monitor) promIsDisabled() float64 {
	if m.svc.IsDisabled() {
		return 1
	}
	return 0
}

func (m *Monitor) promIsTerminating() float64 {
	if m.svc.IsTerminating() {
		return 1
	}
	return 0
}
