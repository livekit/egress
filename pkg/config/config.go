package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	livekit "github.com/livekit/protocol/proto"
)

const Display = ":99"

type Config struct {
	ApiKey     string                    `yaml:"api_key"`
	ApiSecret  string                    `yaml:"api_secret"`
	WsUrl      string                    `yaml:"ws_url"`
	HealthPort int                       `yaml:"health_port"`
	LogLevel   string                    `yaml:"log_level"`
	Redis      RedisConfig               `yaml:"redis"`
	S3         S3Config                  `yaml:"s3"`
	Defaults   *livekit.RecordingOptions `yaml:"defaults"`
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
	Region    string `yaml:"region"`
}

func NewConfig(confString string) (*Config, error) {
	// start with defaults
	conf := &Config{
		LogLevel: "info",
		Defaults: &livekit.RecordingOptions{
			Width:          1920,
			Height:         1080,
			Depth:          24,
			Framerate:      30,
			AudioBitrate:   128,
			AudioFrequency: 44100,
			VideoBitrate:   4500,
		},
	}

	if confString != "" {
		if err := yaml.Unmarshal([]byte(confString), conf); err != nil {
			return nil, fmt.Errorf("could not parse config: %v", err)
		}
	}

	// apply preset options
	if conf.Defaults.Preset != livekit.RecordingPreset_NONE {
		conf.Defaults = fromPreset(conf.Defaults.Preset)
	}

	if err := os.Setenv("DISPLAY", Display); err != nil {
		return nil, err
	}

	var gstDebug int
	switch conf.LogLevel {
	case "debug":
		gstDebug = 3
	case "info", "warn", "error":
		gstDebug = 1
	case "panic":
		gstDebug = 0
	}
	if err := os.Setenv("GST_DEBUG", fmt.Sprint(gstDebug)); err != nil {
		return nil, err
	}

	return conf, nil
}

func TestConfig() *Config {
	return &Config{
		Redis: RedisConfig{
			Address: "localhost:6379",
		},
		Defaults: &livekit.RecordingOptions{
			Width:          1920,
			Height:         1080,
			Depth:          24,
			Framerate:      30,
			AudioBitrate:   128,
			AudioFrequency: 44100,
			VideoBitrate:   4500,
		},
	}
}

func (c *Config) ApplyDefaults(req *livekit.StartRecordingRequest) {
	if req.Options == nil {
		req.Options = &livekit.RecordingOptions{}
	}

	if req.Options.Preset != livekit.RecordingPreset_NONE {
		req.Options = fromPreset(req.Options.Preset)
		return
	}

	if req.Options.Width == 0 || req.Options.Height == 0 {
		req.Options.Width = c.Defaults.Width
		req.Options.Height = c.Defaults.Height
	}
	if req.Options.Depth == 0 {
		req.Options.Depth = c.Defaults.Depth
	}
	if req.Options.Framerate == 0 {
		req.Options.Framerate = c.Defaults.Framerate
	}
	if req.Options.AudioBitrate == 0 {
		req.Options.AudioBitrate = c.Defaults.AudioBitrate
	}
	if req.Options.AudioFrequency == 0 {
		req.Options.AudioFrequency = c.Defaults.AudioFrequency
	}
	if req.Options.VideoBitrate == 0 {
		req.Options.VideoBitrate = c.Defaults.VideoBitrate
	}

	return
}

func fromPreset(preset livekit.RecordingPreset) *livekit.RecordingOptions {
	switch preset {
	case livekit.RecordingPreset_HD_30:
		return &livekit.RecordingOptions{
			Width:          1280,
			Height:         720,
			Depth:          24,
			Framerate:      30,
			AudioBitrate:   128,
			AudioFrequency: 44100,
			VideoBitrate:   3000,
		}
	case livekit.RecordingPreset_HD_60:
		return &livekit.RecordingOptions{
			Width:          1280,
			Height:         720,
			Depth:          24,
			Framerate:      60,
			AudioBitrate:   128,
			AudioFrequency: 44100,
			VideoBitrate:   4500,
		}
	case livekit.RecordingPreset_FULL_HD_30:
		return &livekit.RecordingOptions{
			Width:          1920,
			Height:         1080,
			Depth:          24,
			Framerate:      30,
			AudioBitrate:   128,
			AudioFrequency: 44100,
			VideoBitrate:   4500,
		}
	case livekit.RecordingPreset_FULL_HD_60:
		return &livekit.RecordingOptions{
			Width:          1920,
			Height:         1080,
			Depth:          24,
			Framerate:      60,
			AudioBitrate:   128,
			AudioFrequency: 44100,
			VideoBitrate:   6000,
		}
	default:
		return nil
	}
}
