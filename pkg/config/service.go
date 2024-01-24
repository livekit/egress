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

	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
)

const (
	TmpDir = "/home/egress/tmp"

	roomCompositeCpuCost      = 4
	audioRoomCompositeCpuCost = 1
	webCpuCost                = 4
	audioWebCpuCost           = 1
	participantCpuCost        = 2
	trackCompositeCpuCost     = 1
	trackCpuCost              = 0.5
	maxCpuUtilization         = 0.8

	defaultTemplatePort         = 7980
	defaultTemplateBaseTemplate = "http://localhost:%d/"
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
	MaxCpuUtilization         float64 `yaml:"max_cpu_utilization"` // Maximum allowed CPU utilization when deciding to accept a request. Default to 80%.
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
		},
		TemplatePort:  defaultTemplatePort,
		CPUCostConfig: &CPUCostConfig{},
	}
	if confString != "" {
		if err := yaml.Unmarshal([]byte(confString), conf); err != nil {
			return nil, errors.ErrCouldNotParseConfig(err)
		}
	}

	// always create a new node ID
	conf.NodeID = utils.NewGuid("NE_")

	// Setting CPU costs from config. Ensure that CPU costs are positive
	if conf.RoomCompositeCpuCost <= 0 {
		conf.RoomCompositeCpuCost = roomCompositeCpuCost
	}
	if conf.AudioRoomCompositeCpuCost <= 0 {
		conf.AudioRoomCompositeCpuCost = audioRoomCompositeCpuCost
	}
	if conf.WebCpuCost <= 0 {
		conf.WebCpuCost = webCpuCost
	}
	if conf.AudioWebCpuCost <= 0 {
		conf.AudioWebCpuCost = audioWebCpuCost
	}
	if conf.ParticipantCpuCost <= 0 {
		conf.ParticipantCpuCost = participantCpuCost
	}
	if conf.TrackCompositeCpuCost <= 0 {
		conf.TrackCompositeCpuCost = trackCompositeCpuCost
	}
	if conf.TrackCpuCost <= 0 {
		conf.TrackCpuCost = trackCpuCost
	}
	if conf.MaxCpuUtilization <= 0 || conf.MaxCpuUtilization > 1 {
		conf.MaxCpuUtilization = maxCpuUtilization
	}

	if conf.TemplateBase == "" {
		conf.TemplateBase = fmt.Sprintf(defaultTemplateBaseTemplate, conf.TemplatePort)
	}

	if err := conf.initLogger("nodeID", conf.NodeID, "clusterID", conf.ClusterID); err != nil {
		return nil, err
	}

	return conf, nil
}
