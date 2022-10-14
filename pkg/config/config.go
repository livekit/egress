package config

import (
	"os"
	"path"
	"time"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/redis"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go"
)

const (
	roomCompositeCpuCost  = 3
	trackCompositeCpuCost = 2
	trackCpuCost          = 1
)

type Config struct {
	Redis     *redis.RedisConfig `yaml:"redis"`      // required
	ApiKey    string             `yaml:"api_key"`    // required (env LIVEKIT_API_KEY)
	ApiSecret string             `yaml:"api_secret"` // required (env LIVEKIT_API_SECRET)
	WsUrl     string             `yaml:"ws_url"`     // required (env LIVEKIT_WS_URL)

	HealthPort           int    `yaml:"health_port"`
	PrometheusPort       int    `yaml:"prometheus_port"`
	LogLevel             string `yaml:"log_level"`
	TemplateBase         string `yaml:"template_base"`
	Insecure             bool   `yaml:"insecure"`
	LocalOutputDirectory string `yaml:"local_directory"` // used for temporary storage before upload

	S3    *S3Config    `yaml:"s3"`
	Azure *AzureConfig `yaml:"azure"`
	GCP   *GCPConfig   `yaml:"gcp"`

	// CPU costs for various egress types
	CPUCost CPUCostConfig `yaml:"cpu_cost"`

	SessionLimits `yaml:"session_limits"`

	// internal
	NodeID     string      `yaml:"-"`
	FileUpload interface{} `yaml:"-"` // one of S3, Azure, or GCP
}

type S3Config struct {
	AccessKey      string `yaml:"access_key"` // (env AWS_ACCESS_KEY_ID)
	Secret         string `yaml:"secret"`     // (env AWS_SECRET_ACCESS_KEY)
	Region         string `yaml:"region"`     // (env AWS_DEFAULT_REGION)
	Endpoint       string `yaml:"endpoint"`
	Bucket         string `yaml:"bucket"`
	ForcePathStyle bool   `yaml:"force_path_style"`
}

type AzureConfig struct {
	AccountName   string `yaml:"account_name"` // (env AZURE_STORAGE_ACCOUNT)
	AccountKey    string `yaml:"account_key"`  // (env AZURE_STORAGE_KEY)
	ContainerName string `yaml:"container_name"`
}

type GCPConfig struct {
	CredentialsJSON string `yaml:"credentials_json"` // (env GOOGLE_APPLICATION_CREDENTIALS)
	Bucket          string `yaml:"bucket"`
}

type SessionLimits struct {
	FileOutputMaxDuration    time.Duration `yaml:"file_output_max_duration"`
	StreamOutputMaxDuration  time.Duration `yaml:"stream_output_max_duration"`
	SegmentOutputMaxDuration time.Duration `yaml:"segment_output_max_duration"`
}

type CPUCostConfig struct {
	RoomCompositeCpuCost  float64 `yaml:"room_composite_cpu_cost"`
	TrackCompositeCpuCost float64 `yaml:"track_composite_cpu_cost"`
	TrackCpuCost          float64 `yaml:"track_cpu_cost"`
}

func NewConfig(confString string) (*Config, error) {
	conf := &Config{
		LogLevel:     "info",
		TemplateBase: "https://egress-composite.livekit.io",
		ApiKey:       os.Getenv("LIVEKIT_API_KEY"),
		ApiSecret:    os.Getenv("LIVEKIT_API_SECRET"),
		WsUrl:        os.Getenv("LIVEKIT_WS_URL"),
		NodeID:       utils.NewGuid("NE_"),
	}
	if confString != "" {
		if err := yaml.Unmarshal([]byte(confString), conf); err != nil {
			return nil, errors.ErrCouldNotParseConfig(err)
		}
	}

	if conf.S3 != nil {
		conf.FileUpload = &livekit.S3Upload{
			AccessKey:      conf.S3.AccessKey,
			Secret:         conf.S3.Secret,
			Region:         conf.S3.Region,
			Endpoint:       conf.S3.Endpoint,
			Bucket:         conf.S3.Bucket,
			ForcePathStyle: conf.S3.ForcePathStyle,
		}
	} else if conf.GCP != nil {
		var credentials []byte
		if conf.GCP.CredentialsJSON != "" {
			credentials = []byte(conf.GCP.CredentialsJSON)
		}
		conf.FileUpload = &livekit.GCPUpload{
			Credentials: credentials,
			Bucket:      conf.GCP.Bucket,
		}
	} else if conf.Azure != nil {
		conf.FileUpload = &livekit.AzureBlobUpload{
			AccountName:   conf.Azure.AccountName,
			AccountKey:    conf.Azure.AccountKey,
			ContainerName: conf.Azure.ContainerName,
		}
	}

	// Setting CPU costs from config. Ensure that CPU costs are positive
	if conf.CPUCost.RoomCompositeCpuCost <= 0 {
		conf.CPUCost.RoomCompositeCpuCost = roomCompositeCpuCost
	}
	if conf.CPUCost.TrackCompositeCpuCost <= 0 {
		conf.CPUCost.TrackCompositeCpuCost = trackCompositeCpuCost
	}
	if conf.CPUCost.TrackCpuCost <= 0 {
		conf.CPUCost.TrackCpuCost = trackCpuCost
	}

	conf.LocalOutputDirectory = path.Clean(conf.LocalOutputDirectory)
	if conf.LocalOutputDirectory == "." {
		conf.LocalOutputDirectory = os.TempDir()
	}

	if err := conf.initLogger(); err != nil {
		return nil, err
	}

	return conf, nil
}

func (c *Config) initLogger() error {
	conf := zap.NewProductionConfig()
	if c.LogLevel != "" {
		lvl := zapcore.Level(0)
		if err := lvl.UnmarshalText([]byte(c.LogLevel)); err == nil {
			conf.Level = zap.NewAtomicLevelAt(lvl)
		}
	}

	l, _ := conf.Build()

	logger.SetLogger(zapr.NewLogger(l).WithValues("nodeID", c.NodeID), "egress")
	lksdk.SetLogger(logger.GetLogger())
	return nil
}
