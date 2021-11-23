package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	livekit "github.com/livekit/protocol/proto"
)

const Display = ":99"

type Config struct {
	ApiKey          string      `yaml:"api_key"`
	ApiSecret       string      `yaml:"api_secret"`
	WsUrl           string      `yaml:"ws_url"`
	HealthPort      int         `yaml:"health_port"`
	LogLevel        string      `yaml:"log_level"`
	TemplateAddress string      `yaml:"template_address"`
	Redis           RedisConfig `yaml:"redis"`
	FileOutput      FileOutput  `yaml:"file_output"`
	Defaults        Defaults    `yaml:"defaults"`
}

type RedisConfig struct {
	Address  string `yaml:"address"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type FileOutput struct {
	Local  bool          `yaml:"local"`
	S3     *S3Config     `yaml:"s3"`
	Azblob *AzblobConfig `yaml:"azblob"`
}

type S3Config struct {
	AccessKey string `yaml:"access_key"`
	Secret    string `yaml:"secret"`
	Endpoint  string `yaml:"endpoint"`
	Region    string `yaml:"region"`
	Bucket    string `yaml:"bucket"`
}

type AzblobConfig struct {
	AccountName   string `yaml:"account_name"`
	AccountKey    string `yaml:"account_key"`
	ContainerName string `yaml:"container_name"`
}

type Defaults struct {
	Preset         livekit.RecordingPreset `yaml:"preset"`
	Width          int32                   `yaml:"width"`
	Height         int32                   `yaml:"height"`
	Depth          int32                   `yaml:"depth"`
	Framerate      int32                   `yaml:"framerate"`
	AudioBitrate   int32                   `yaml:"audio_bitrate"`
	AudioFrequency int32                   `yaml:"audio_frequency"`
	VideoBitrate   int32                   `yaml:"video_bitrate"`
}

func NewConfig(confString string) (*Config, error) {
	// start with defaults
	conf := &Config{
		LogLevel:        "info",
		TemplateAddress: "https://recorder.livekit.io",
		Defaults: Defaults{
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
	if conf.FileOutput.S3 == nil && conf.FileOutput.Azblob == nil {
		conf.FileOutput.Local = true
	}

	if conf.Defaults.Preset != livekit.RecordingPreset_NONE {
		conf.Defaults = fromProto(fromPreset(conf.Defaults.Preset))
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
		TemplateAddress: "https://recorder.livekit.io",
		Redis: RedisConfig{
			Address: "localhost:6379",
		},
		Defaults: Defaults{
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
	} else if req.Options.Preset != livekit.RecordingPreset_NONE {
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
		return &livekit.RecordingOptions{}
	}
}

func fromProto(opts *livekit.RecordingOptions) Defaults {
	return Defaults{
		Width:          opts.Width,
		Height:         opts.Height,
		Depth:          opts.Depth,
		Framerate:      opts.Framerate,
		AudioBitrate:   opts.AudioBitrate,
		AudioFrequency: opts.AudioFrequency,
		VideoBitrate:   opts.VideoBitrate,
	}
}
