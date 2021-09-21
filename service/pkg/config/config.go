package config

import (
	"fmt"

	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"

	livekit "github.com/livekit/protocol/proto"
)

type Config struct {
	Redis      RedisConfig               `yaml:"redis"`
	ApiKey     string                    `yaml:"api_key"`
	ApiSecret  string                    `yaml:"api_secret"`
	WsUrl      string                    `yaml:"ws_url"`
	S3         S3Config                  `yaml:"s3"`
	HealthPort int                       `yaml:"health_port"`
	Options    *livekit.RecordingOptions `yaml:"options"`
	LogLevel   string                    `yaml:"log_level"`
	Test       bool                      `yaml:"-"`
}

type RedisConfig struct {
	Address  string `yaml:"address"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type S3Config struct {
	AccessKey string `yaml:"access_key"`
	Secret    string `yaml:"secret"`
}

func NewConfig(confString string, c *cli.Context) (*Config, error) {
	// start with defaults
	conf := &Config{
		Redis: RedisConfig{},
		Options: &livekit.RecordingOptions{
			InputWidth:     1920,
			InputHeight:    1080,
			Depth:          24,
			Framerate:      25,
			AudioBitrate:   128,
			AudioFrequency: 44100,
			VideoBitrate:   4500,
		},
		LogLevel: "debug",
	}

	if confString != "" {
		if err := yaml.Unmarshal([]byte(confString), conf); err != nil {
			return nil, fmt.Errorf("could not parse config: %v", err)
		}
	}

	if c != nil {
		if err := conf.updateFromCLI(c); err != nil {
			return nil, err
		}
	}

	return conf, nil
}

func TestConfig() *Config {
	return &Config{
		Redis: RedisConfig{
			Address: "localhost:6379",
		},
		Options: &livekit.RecordingOptions{
			InputWidth:     1920,
			InputHeight:    1080,
			Depth:          24,
			Framerate:      25,
			AudioBitrate:   128,
			AudioFrequency: 44100,
			VideoBitrate:   4500,
		},
		Test: true,
	}
}

func (conf *Config) updateFromCLI(c *cli.Context) error {
	if c.IsSet("redis-host") {
		conf.Redis.Address = c.String("redis-host")
	}

	return nil
}
