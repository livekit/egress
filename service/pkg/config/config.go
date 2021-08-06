package config

import (
	"encoding/json"
	"fmt"

	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"

	livekit "github.com/livekit/livekit-recorder/service/proto"
)

type Config struct {
	Redis      RedisConfig               `yaml:"redis" json:"-"`
	HealthPort int                       `yaml:"health_port" json:"-"`
	ApiKey     string                    `yaml:"api_key" json:"api_key,omitempty"`
	ApiSecret  string                    `yaml:"api_secret" json:"api_secret,omitempty"`
	Options    *livekit.RecordingOptions `yaml:"options" json:"options"`
	LogLevel   string                    `yaml:"log_level" json:"-"`
	Test       bool                      `yaml:"-" json:"-"`
}

type RedisConfig struct {
	Address  string `yaml:"address"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
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

func Merge(defaults *Config, res *livekit.RecordingReservation) (string, error) {
	var m map[string]interface{}
	if defaults.ApiKey != "" && defaults.ApiSecret != "" {
		m = map[string]interface{}{
			"api_key":    defaults.ApiKey,
			"api_secret": defaults.ApiSecret,
		}
	} else {
		m = make(map[string]interface{})
	}

	req := res.Request
	switch input := req.Input.(type) {
	case *livekit.StartRecordingRequest_Url:
		m["input"] = map[string]interface{}{"url": input.Url}
	case *livekit.StartRecordingRequest_Template:
		m["input"] = map[string]interface{}{"template": input.Template}
	}

	switch output := req.Output.(type) {
	case *livekit.StartRecordingRequest_File:
		m["output"] = map[string]interface{}{"file": output.File}
	case *livekit.StartRecordingRequest_S3:
		m["output"] = map[string]interface{}{"s3": output.S3}
	case *livekit.StartRecordingRequest_Rtmp:
		m["output"] = map[string]interface{}{"rtmp": output.Rtmp}
	}

	if req.Options != nil {
		if req.Options.Preset != "" {
			m["options"] = map[string]interface{}{"preset": req.Options.Preset}
		} else {
			// combine options
			options := make(map[string]interface{})

			jsonDefaults, err := json.Marshal(defaults.Options)
			if err != nil {
				return "", err
			}
			err = json.Unmarshal(jsonDefaults, &options)
			if err != nil {
				return "", err
			}

			jsonReq, err := json.Marshal(req.Options)
			if err != nil {
				return "", err
			}
			err = json.Unmarshal(jsonReq, &options)
			if err != nil {
				return "", err
			}

			m["options"] = options
		}
	} else {
		m["options"] = defaults.Options
	}

	b, err := json.Marshal(m)
	return string(b), err
}
