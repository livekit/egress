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

package config

import (
	"fmt"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

const (
	roomCompositeCpuCost      = 4
	audioRoomCompositeCpuCost = 1
	webCpuCost                = 4
	audioWebCpuCost           = 1
	participantCpuCost        = 2
	trackCompositeCpuCost     = 1
	trackCpuCost              = 0.5
	maxCpuUtilization         = 0.8
	maxUploadQueue            = 60

	defaultTemplatePort         = 7980
	defaultTemplateBaseTemplate = "http://localhost:%d/"

	defaultIOCreateTimeout = time.Second * 15
	defaultIOUpdateTimeout = time.Second * 30
	defaultIOWorkers       = 5

	defaultJitterBufferLatency   = time.Second * 2
	defaultAudioMixerLatency     = time.Millisecond * 2750
	defaultPipelineLatency       = time.Second * 3
	defaultRTPMaxDriftAdjustment = time.Millisecond * 5
	defaultOldPacketThreshold    = 500 * time.Millisecond
	defaultRTPMaxAllowedTsDiff   = time.Second * 5

	defaultAudioTempoControllerAdjustmentRate = 0.05

	defaultMaxPulseClients = 60
)

type ServiceConfig struct {
	BaseConfig `yaml:",inline"`

	HealthPort       int `yaml:"health_port"`        // health check port
	TemplatePort     int `yaml:"template_port"`      // room composite template server port
	PrometheusPort   int `yaml:"prometheus_port"`    // prometheus handler port
	DebugHandlerPort int `yaml:"debug_handler_port"` // egress debug handler port

	*CPUCostConfig `yaml:"cpu_cost"` // CPU costs for the different egress types
}

type CPUCostConfig struct {
	MaxCpuUtilization         float64 `yaml:"max_cpu_utilization"` // maximum allowed CPU utilization when deciding to accept a request. Default to 80%
	MaxMemory                 float64 `yaml:"max_memory"`          // maximum allowed memory usage in GB. 0 to disable
	MemoryCost                float64 `yaml:"memory_cost"`         // minimum memory in GB
	RoomCompositeCpuCost      float64 `yaml:"room_composite_cpu_cost"`
	AudioRoomCompositeCpuCost float64 `yaml:"audio_room_composite_cpu_cost"`
	WebCpuCost                float64 `yaml:"web_cpu_cost"`
	AudioWebCpuCost           float64 `yaml:"audio_web_cpu_cost"`
	ParticipantCpuCost        float64 `yaml:"participant_cpu_cost"`
	TrackCompositeCpuCost     float64 `yaml:"track_composite_cpu_cost"`
	TrackCpuCost              float64 `yaml:"track_cpu_cost"`
	MaxPulseClients           int     `yaml:"max_pulse_clients"` // pulse client limit for launching chrome
}

func NewServiceConfig(confString string) (*ServiceConfig, error) {
	conf := &ServiceConfig{
		BaseConfig: BaseConfig{
			Logging: &logger.Config{
				Level: "info",
			},
			ApiKey:    os.Getenv("LIVEKIT_API_KEY"),
			ApiSecret: os.Getenv("LIVEKIT_API_SECRET"),
			WsUrl:     os.Getenv("LIVEKIT_WS_URL"),
		},
		CPUCostConfig: &CPUCostConfig{},
	}
	if confString != "" {
		if err := yaml.Unmarshal([]byte(confString), conf); err != nil {
			return nil, errors.ErrCouldNotParseConfig(err)
		}
	}

	// always create a new node ID
	conf.NodeID = utils.NewGuid("NE_")
	conf.InitDefaults()

	rpc.InitPSRPCStats(prometheus.Labels{"node_id": conf.NodeID, "node_type": "EGRESS"})

	if err := conf.initLogger("nodeID", conf.NodeID, "clusterID", conf.ClusterID); err != nil {
		return nil, err
	}

	return conf, nil
}

func (c *ServiceConfig) InitDefaults() {
	if c.CPUCostConfig == nil {
		c.CPUCostConfig = new(CPUCostConfig)
	}

	if c.TemplatePort == 0 {
		c.TemplatePort = defaultTemplatePort
	}
	if c.TemplateBase == "" {
		c.TemplateBase = fmt.Sprintf(defaultTemplateBaseTemplate, c.TemplatePort)
	}

	if c.IOCreateTimeout == 0 {
		c.IOCreateTimeout = defaultIOCreateTimeout
	}
	if c.IOUpdateTimeout == 0 {
		c.IOUpdateTimeout = defaultIOUpdateTimeout
	}
	if c.IOWorkers <= 0 {
		c.IOWorkers = defaultIOWorkers
	}

	// Setting CPU costs from config. Ensure that CPU costs are positive
	if c.MaxCpuUtilization <= 0 || c.MaxCpuUtilization > 1 {
		c.MaxCpuUtilization = maxCpuUtilization
	}
	if c.RoomCompositeCpuCost <= 0 {
		c.RoomCompositeCpuCost = roomCompositeCpuCost
	}
	if c.AudioRoomCompositeCpuCost <= 0 {
		c.AudioRoomCompositeCpuCost = audioRoomCompositeCpuCost
	}
	if c.WebCpuCost <= 0 {
		c.WebCpuCost = webCpuCost
	}
	if c.AudioWebCpuCost <= 0 {
		c.AudioWebCpuCost = audioWebCpuCost
	}
	if c.ParticipantCpuCost <= 0 {
		c.ParticipantCpuCost = participantCpuCost
	}
	if c.TrackCompositeCpuCost <= 0 {
		c.TrackCompositeCpuCost = trackCompositeCpuCost
	}
	if c.TrackCpuCost <= 0 {
		c.TrackCpuCost = trackCpuCost
	}
	if c.MaxPulseClients == 0 {
		c.MaxPulseClients = defaultMaxPulseClients
	}

	if c.MaxUploadQueue <= 0 {
		c.MaxUploadQueue = maxUploadQueue
	}

	applyLatencyDefaults(&c.Latency)

	if c.AudioTempoController.Enabled {
		if c.AudioTempoController.AdjustmentRate > 0.2 || c.AudioTempoController.AdjustmentRate <= 0 {
			c.AudioTempoController.AdjustmentRate = defaultAudioTempoControllerAdjustmentRate
		}
	}
}

func applyLatencyDefaults(latency *LatencyConfig) {
	if latency.JitterBufferLatency == 0 {
		latency.JitterBufferLatency = defaultJitterBufferLatency
	}
	if latency.AudioMixerLatency == 0 {
		latency.AudioMixerLatency = defaultAudioMixerLatency
	}
	if latency.PipelineLatency == 0 {
		latency.PipelineLatency = defaultPipelineLatency
	}
	if latency.RTPMaxAllowedTsDiff == 0 {
		latency.RTPMaxAllowedTsDiff = defaultRTPMaxAllowedTsDiff
	}
	if latency.RTPMaxAllowedTsDiff < latency.JitterBufferLatency {
		// RTP max allowed ts diff must be equal or greater than jitter buffer latency to absorb the jitter buffer burst
		latency.RTPMaxAllowedTsDiff = latency.JitterBufferLatency
	}
	if latency.RTPMaxDriftAdjustment == 0 {
		latency.RTPMaxDriftAdjustment = defaultRTPMaxDriftAdjustment
	}
	if latency.OldPacketThreshold == 0 {
		latency.OldPacketThreshold = defaultOldPacketThreshold
	}
}
