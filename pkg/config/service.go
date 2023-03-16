package config

import (
	"fmt"
	"os"
	"path"

	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/utils"
)

const (
	roomCompositeCpuCost  = 4.5
	webCpuCost            = 4.5
	trackCompositeCpuCost = 2
	trackCpuCost          = 1

	defaultTemplatePort         = 7980
	defaultTemplateBaseTemplate = "http://localhost:%d/"
)

type ServiceConfig struct {
	BaseConfig `yaml:",inline"`

	HealthPort       int `yaml:"health_port"`
	TemplatePort     int `yaml:"template_port"`
	PrometheusPort   int `yaml:"prometheus_port"`
	DebugHandlerPort int `yaml:"debug_handler_port"` // Port used to launch the egress debug handler. 0 means debug handler disabled.

	CPUCostConfig `yaml:"cpu_cost"` // CPU costs for various egress types
}

type CPUCostConfig struct {
	RoomCompositeCpuCost  float64 `yaml:"room_composite_cpu_cost"`
	TrackCompositeCpuCost float64 `yaml:"track_composite_cpu_cost"`
	TrackCpuCost          float64 `yaml:"track_cpu_cost"`
	WebCpuCost            float64 `yaml:"web_cpu_cost"`
}

func NewServiceConfig(confString string) (*ServiceConfig, error) {
	conf := &ServiceConfig{
		BaseConfig: BaseConfig{
			ApiKey:    os.Getenv("LIVEKIT_API_KEY"),
			ApiSecret: os.Getenv("LIVEKIT_API_SECRET"),
			WsUrl:     os.Getenv("LIVEKIT_WS_URL"),
			LogLevel:  "info",
		},
		TemplatePort: defaultTemplatePort,
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
	if conf.WebCpuCost <= 0 {
		conf.WebCpuCost = webCpuCost
	}
	if conf.TrackCompositeCpuCost <= 0 {
		conf.TrackCompositeCpuCost = trackCompositeCpuCost
	}
	if conf.TrackCpuCost <= 0 {
		conf.TrackCpuCost = trackCpuCost
	}

	if conf.TemplateBase == "" {
		conf.TemplateBase = fmt.Sprintf(defaultTemplateBaseTemplate, conf.TemplatePort)
	}

	conf.LocalOutputDirectory = path.Clean(conf.LocalOutputDirectory)
	if conf.LocalOutputDirectory == "." {
		conf.LocalOutputDirectory = os.TempDir()
	}

	if err := conf.initLogger("nodeID", conf.NodeID, "clusterID", conf.ClusterID); err != nil {
		return nil, err
	}

	return conf, nil
}
