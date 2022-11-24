package config

import (
	"time"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/redis"
	lksdk "github.com/livekit/server-sdk-go"
)

type BaseConfig struct {
	NodeID string // will be overwritten

	Redis                *redis.RedisConfig `yaml:"redis"`      // required
	ApiKey               string             `yaml:"api_key"`    // required (env LIVEKIT_API_KEY)
	ApiSecret            string             `yaml:"api_secret"` // required (env LIVEKIT_API_SECRET)
	WsUrl                string             `yaml:"ws_url"`     // required (env LIVEKIT_WS_URL)
	LogLevel             string             `yaml:"log_level"`
	TemplateBase         string             `yaml:"template_base"`
	Insecure             bool               `yaml:"insecure"`
	LocalOutputDirectory string             `yaml:"local_directory"` // used for temporary storage before upload

	S3     *S3Config    `yaml:"s3"`
	Azure  *AzureConfig `yaml:"azure"`
	GCP    *GCPConfig   `yaml:"gcp"`
	AliOSS *S3Config    `yaml:"alioss"`

	SessionLimits `yaml:"session_limits"`
}

type S3Config struct {
	AccessKey      string `yaml:"access_key"` // (env AWS_ACCESS_KEY_ID)
	Secret         string `yaml:"secret"`     // (env AWS_SECRET_ACCESS_KEY)
	Region         string `yaml:"region"`     // (env AWS_DEFAULT_REGION)
	Endpoint       string `yaml:"endpoint"`
	Bucket         string `yaml:"bucket"`
	ForcePathStyle bool   `yaml:"force_path_style"`
}

func (c *S3Config) ToS3Upload() *livekit.S3Upload {
	return &livekit.S3Upload{
		AccessKey:      c.AccessKey,
		Secret:         c.Secret,
		Region:         c.Region,
		Endpoint:       c.Endpoint,
		Bucket:         c.Bucket,
		ForcePathStyle: c.ForcePathStyle,
	}
}

func (c *S3Config) ToAliOSSUpload() *livekit.AliOSSUpload {
	return &livekit.AliOSSUpload{
		AccessKey: c.AccessKey,
		Secret:    c.Secret,
		Region:    c.Region,
		Endpoint:  c.Endpoint,
		Bucket:    c.Bucket,
	}
}

type AzureConfig struct {
	AccountName   string `yaml:"account_name"` // (env AZURE_STORAGE_ACCOUNT)
	AccountKey    string `yaml:"account_key"`  // (env AZURE_STORAGE_KEY)
	ContainerName string `yaml:"container_name"`
}

func (c *AzureConfig) ToAzureUpload() *livekit.AzureBlobUpload {
	return &livekit.AzureBlobUpload{
		AccountName:   c.AccountName,
		AccountKey:    c.AccountKey,
		ContainerName: c.ContainerName,
	}
}

type GCPConfig struct {
	CredentialsJSON string `yaml:"credentials_json"` // (env GOOGLE_APPLICATION_CREDENTIALS)
	Bucket          string `yaml:"bucket"`
}

func (c *GCPConfig) ToGCPUpload() *livekit.GCPUpload {
	return &livekit.GCPUpload{
		Credentials: []byte(c.CredentialsJSON),
		Bucket:      c.Bucket,
	}
}

type SessionLimits struct {
	FileOutputMaxDuration    time.Duration `yaml:"file_output_max_duration"`
	StreamOutputMaxDuration  time.Duration `yaml:"stream_output_max_duration"`
	SegmentOutputMaxDuration time.Duration `yaml:"segment_output_max_duration"`
}

func (c *BaseConfig) initLogger(values ...interface{}) error {
	conf := zap.NewProductionConfig()
	if c.LogLevel != "" {
		lvl := zapcore.Level(0)
		if err := lvl.UnmarshalText([]byte(c.LogLevel)); err == nil {
			conf.Level = zap.NewAtomicLevelAt(lvl)
		}
	}

	l, _ := conf.Build()

	logger.SetLogger(zapr.NewLogger(l).WithValues(values...), "egress")
	lksdk.SetLogger(logger.GetLogger())
	return nil
}
