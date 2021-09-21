package config

import (
	"encoding/json"
	"errors"
	"strings"

	livekit "github.com/livekit/protocol/proto"
)

type Request struct {
	ApiKey    string         `json:"api_key,omitempty"`
	ApiSecret string         `json:"api_secret,omitempty"`
	Input     *RequestInput  `json:"input,omitempty"`
	Output    *RequestOutput `json:"output,omitempty"`
	Options   interface{}    `json:"options,omitempty"`
}

type RequestInput struct {
	Template *RequestTemplate `json:"template,omitempty"`
	Url      string           `json:"url,omitempty"`
}

type RequestTemplate struct {
	Layout   string `json:"layout,omitempty"`
	WsUrl    string `json:"ws_url,omitempty"`
	Token    string `json:"token,omitempty"`
	RoomName string `json:"room_name,omitempty"`
}

type RequestOutput struct {
	S3   *RequestS3 `json:"s3,omitempty"`
	Rtmp string     `json:"rtmp,omitempty"`
}

type RequestS3 struct {
	Bucket    string `json:"bucket,omitempty"`
	Key       string `json:"key,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	Secret    string `json:"secret,omitempty"`
}

// Creates a recorder config by combining service defaults with reservation request
func Merge(defaults *Config, res *livekit.RecordingReservation) (string, error) {
	req := res.Request
	if req.Input == nil || req.Output == nil {
		return "", errors.New("input and output required")
	}

	conf := &Request{
		ApiKey:    defaults.ApiKey,
		ApiSecret: defaults.ApiSecret,
		Input: &RequestInput{
			Url: req.Input.Url,
		},
		Output: &RequestOutput{
			Rtmp: req.Output.Rtmp,
		},
	}

	if req.Input.Template != nil {
		conf.Input.Template = &RequestTemplate{
			Layout:   req.Input.Template.Layout,
			WsUrl:    defaults.WsUrl,
			Token:    req.Input.Template.Token,
			RoomName: req.Input.Template.RoomName,
		}
	}

	if idx := strings.Index(req.Output.S3Path, "/"); idx != -1 {
		conf.Output.S3 = &RequestS3{
			Bucket:    req.Output.S3Path[:idx],
			Key:       req.Output.S3Path[idx+1:],
			AccessKey: defaults.S3.AccessKey,
			Secret:    defaults.S3.Secret,
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
