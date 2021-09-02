package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pkg/errors"
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

// Creates a recorder config by combining service defaults with reservation request
func Merge(defaults *Config, res *livekit.RecordingReservation) (string, error) {
	req := res.Request
	if req.Input == nil || req.Output == nil {
		return "", errors.New("input and output required")
	}

	conf := struct {
		ApiKey    string      `json:"api_key,omitempty"`
		ApiSecret string      `json:"api_secret,omitempty"`
		Input     interface{} `json:"input,omitempty"`
		Output    interface{} `json:"output,omitempty"`
		Options   interface{} `json:"options,omitempty"`
	}{
		ApiKey:    defaults.ApiKey,
		ApiSecret: defaults.ApiSecret,
	}

	if req.Input.Template != nil {
		conf.Input = struct {
			Template interface{} `json:"template,omitempty"`
		}{
			Template: struct {
				Layout   string `json:"layout,omitempty"`
				WsUrl    string `json:"ws_url,omitempty"`
				Token    string `json:"token,omitempty"`
				RoomName string `json:"room_name,omitempty"`
			}{
				Layout:   req.Input.Template.Layout,
				WsUrl:    defaults.WsUrl,
				Token:    req.Input.Template.Token,
				RoomName: req.Input.Template.RoomName,
			},
		}
	} else {
		conf.Input = struct {
			Url string `json:"url,omitempty"`
		}{
			Url: req.Input.Url,
		}
	}

	if idx := strings.Index(req.Output.S3Path, "/"); idx > 0 {
		conf.Output = struct {
			S3 interface{} `json:"s3,omitempty"`
		}{
			S3: struct {
				Bucket    string `json:"bucket,omitempty"`
				Key       string `json:"key,omitempty"`
				AccessKey string `json:"access_key,omitempty"`
				Secret    string `json:"secret,omitempty"`
			}{
				Bucket:    req.Output.S3Path[:idx],
				Key:       req.Output.S3Path[idx+1:],
				AccessKey: defaults.S3.AccessKey,
				Secret:    defaults.S3.Secret,
			},
		}
	} else {
		conf.Output = struct {
			Rtmp interface{} `json:"rtmp,omitempty"`
		}{
			Rtmp: req.Output.Rtmp,
		}
	}

	if req.Options != nil {
		if req.Options.Preset != livekit.RecordingPreset_NONE {
			conf.Options = map[string]interface{}{"preset": req.Options.Preset}
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

			conf.Options = options
		}
	} else {
		conf.Options = defaults.Options
	}

	b, err := json.Marshal(conf)
	return string(b), err
}
