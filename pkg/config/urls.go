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
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-jose/go-jose/v3/json"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/utils"
)

var twitchEndpoint = regexp.MustCompile("^rtmps?://.*\\.contribute\\.live-video\\.net/app/(.*)( live=1)?$")

func (o *StreamConfig) ValidateUrl(rawUrl string, outputType types.OutputType) (string, string, error) {
	parsed, err := url.Parse(rawUrl)
	if err != nil {
		return "", "", errors.ErrInvalidUrl(rawUrl, err.Error())
	}
	if types.StreamOutputTypes[parsed.Scheme] != outputType {
		return "", "", errors.ErrInvalidUrl(rawUrl, "invalid scheme")
	}

	switch outputType {
	case types.OutputTypeRTMP:
		if parsed.Scheme == "mux" {
			rawUrl = fmt.Sprintf("rtmps://global-live.mux.com:443/app/%s", parsed.Host)
		} else if parsed.Scheme == "twitch" {
			rawUrl, err = o.updateTwitchURL(parsed.Host)
			if err != nil {
				return "", "", errors.ErrInvalidUrl(rawUrl, err.Error())
			}
		} else if match := twitchEndpoint.FindStringSubmatch(rawUrl); len(match) > 0 {
			updated, err := o.updateTwitchURL(match[1])
			if err == nil {
				rawUrl = updated
			}
		}

		redacted, ok := utils.RedactStreamKey(rawUrl)
		if !ok {
			return "", "", errors.ErrInvalidUrl(rawUrl, "rtmp urls must be of format rtmp(s)://{host}(/{path})/{app}/{stream_key}( live=1)")
		}
		return rawUrl, redacted, nil

	case types.OutputTypeSRT:
		return rawUrl, rawUrl, nil

	case types.OutputTypeRaw:
		return rawUrl, rawUrl, nil

	default:
		return "", "", errors.ErrInvalidInput("stream output type")
	}
}

func (o *StreamConfig) GetStreamUrl(rawUrl string) (string, error) {
	parsed, err := url.Parse(rawUrl)
	if err != nil {
		return "", errors.ErrInvalidUrl(rawUrl, err.Error())
	}

	var twitchKey string
	if parsed.Scheme == "mux" {
		return fmt.Sprintf("rtmps://global-live.mux.com:443/app/%s", parsed.Host), nil
	} else if parsed.Scheme == "twitch" {
		twitchKey = parsed.Host
	} else if match := twitchEndpoint.FindStringSubmatch(rawUrl); len(match) > 0 {
		twitchKey = match[1]
	} else {
		return rawUrl, nil
	}

	// find twitch url by stream key because we can't rely on the ingest endpoint returning consistent results
	for u := range o.StreamInfo {
		if match := twitchEndpoint.FindStringSubmatch(u); len(match) > 0 && match[1] == twitchKey {
			return u, nil
		}
	}

	return "", errors.ErrStreamNotFound(rawUrl)
}

func (o *StreamConfig) updateTwitchURL(key string) (string, error) {
	if err := o.updateTwitchTemplate(); err != nil {
		return "", err
	}

	return strings.ReplaceAll(o.twitchTemplate, "{stream_key}", key), nil
}

func (o *StreamConfig) updateTwitchTemplate() error {
	if o.twitchTemplate != "" {
		return nil
	}

	resp, err := http.Get("https://ingest.twitch.tv/ingests")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var body struct {
		Ingests []struct {
			Name              string `json:"name"`
			URLTemplate       string `json:"url_template"`
			URLTemplateSecure string `json:"url_template_secure"`
			Priority          int    `json:"priority"`
		} `json:"ingests"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}

	for _, ingest := range body.Ingests {
		if ingest.URLTemplateSecure != "" {
			o.twitchTemplate = ingest.URLTemplateSecure
			return nil
		} else if ingest.URLTemplate != "" {
			o.twitchTemplate = ingest.URLTemplate
			return nil
		}
	}

	return errors.New("no ingest found")
}
