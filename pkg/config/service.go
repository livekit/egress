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

	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
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
	maxConcurrentWeb          = 18
	maxUploadQueue            = 60

	defaultTemplatePort         = 7980
	defaultTemplateBaseTemplate = "http://localhost:%d/"

	defaultIOCreateTimeout = time.Second * 15
	defaultIOUpdateTimeout = time.Second * 30

	defaultJitterBufferLatency = time.Second * 2
	defaultAudioMixerLatency   = time.Millisecond * 2500
	defaultPipelineLatency     = time.Second * 3
	defaultAppSrcDrainTimeout  = time.Millisecond * 3500
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
	MaxConcurrentWeb          int32   `yaml:"max_concurrent_web"`  // maximum allowed chrome/x/pulse instances
	RoomCompositeCpuCost      float64 `yaml:"room_composite_cpu_cost"`
	AudioRoomCompositeCpuCost float64 `yaml:"audio_room_composite_cpu_cost"`
	WebCpuCost                float64 `yaml:"web_cpu_cost"`
	AudioWebCpuCost           float64 `yaml:"audio_web_cpu_cost"`
	ParticipantCpuCost        float64 `yaml:"participant_cpu_cost"`
	TrackCompositeCpuCost     float64 `yaml:"track_composite_cpu_cost"`
	TrackCpuCost              float64 `yaml:"track_cpu_cost"`
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
			Latency: LatencyConfig{
				JitterBufferLatency: defaultJitterBufferLatency,
				AudioMixerLatency:   defaultAudioMixerLatency,
				PipelineLatency:     defaultPipelineLatency,
				AppSrcDrainTimeout:  defaultAppSrcDrainTimeout,
			},
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

	// Setting CPU costs from config. Ensure that CPU costs are positive
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
	if c.MaxCpuUtilization <= 0 || c.MaxCpuUtilization > 1 {
		c.MaxCpuUtilization = maxCpuUtilization
	}
	if c.MaxConcurrentWeb <= 0 {
		c.MaxConcurrentWeb = maxConcurrentWeb
	}
	if c.MaxUploadQueue <= 0 {
		c.MaxUploadQueue = maxUploadQueue
	}
}
