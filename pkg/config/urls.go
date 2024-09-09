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
	"time"

	"github.com/go-jose/go-jose/v4/json"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

// rtmp urls must be of format rtmp(s)://{host}(/{path})/{app}/{stream_key}( live=1)
var (
	rtmpRegexp     = regexp.MustCompile("^(rtmps?:\\/\\/)(.*\\/)(.*\\/)(\\S*)( live=1)?$")
	twitchEndpoint = regexp.MustCompile("^rtmps?://.*\\.contribute\\.live-video\\.net/app/(.*)( live=1)?$")
)

func (o *StreamConfig) AddStream(rawUrl string, outputType types.OutputType) (*Stream, error) {
	parsed, redacted, streamID, err := o.ValidateUrl(rawUrl, outputType)
	if err != nil {
		return nil, err
	}

	stream := &Stream{
		ParsedUrl:   parsed,
		RedactedUrl: redacted,
		StreamID:    streamID,
		StreamInfo: &livekit.StreamInfo{
			Url:    redacted,
			Status: livekit.StreamInfo_ACTIVE,
		},
	}
	if outputType != types.OutputTypeRTMP {
		stream.StreamInfo.StartedAt = time.Now().UnixNano()
	}
	o.Streams.Store(parsed, stream)

	return stream, nil
}

func (o *StreamConfig) ValidateUrl(rawUrl string, outputType types.OutputType) (
	parsed string, redacted string, streamID string, err error,
) {
	parsedUrl, err := url.Parse(rawUrl)
	if err != nil {
		err = errors.ErrInvalidUrl(rawUrl, err.Error())
		return
	}
	if types.StreamOutputTypes[parsedUrl.Scheme] != outputType {
		err = errors.ErrInvalidUrl(rawUrl, "invalid scheme")
		return
	}

	switch outputType {
	case types.OutputTypeRTMP:
		if parsedUrl.Scheme == "mux" {
			parsed = fmt.Sprintf("rtmps://global-live.mux.com:443/app/%s", parsedUrl.Host)
		} else if parsedUrl.Scheme == "twitch" {
			parsed, err = o.updateTwitchURL(parsedUrl.Host)
			if err != nil {
				return
			}
		} else if match := twitchEndpoint.FindStringSubmatch(rawUrl); len(match) > 0 {
			if updated, err := o.updateTwitchURL(match[1]); err == nil {
				parsed = updated
			}
		} else {
			parsed = rawUrl
		}

		var ok bool
		redacted, streamID, ok = redactStreamKey(parsed)
		if !ok {
			err = errors.ErrInvalidUrl(rawUrl, "rtmp urls must be of format rtmp(s)://{host}(/{path})/{app}/{stream_key}( live=1)")
		}
		return

	case types.OutputTypeSRT:
		parsed = rawUrl
		redacted = rawUrl
		return

	case types.OutputTypeRaw:
		parsed = rawUrl
		redacted = rawUrl
		return

	default:
		err = errors.ErrInvalidInput("stream output type")
		return
	}
}

func (o *StreamConfig) GetStream(rawUrl string) (*Stream, error) {
	parsedUrl, err := url.Parse(rawUrl)
	if err != nil {
		return nil, errors.ErrInvalidUrl(rawUrl, err.Error())
	}

	var parsed string
	if parsedUrl.Scheme == "mux" {
		parsed = fmt.Sprintf("rtmps://global-live.mux.com:443/app/%s", parsedUrl.Host)
	} else if parsedUrl.Scheme == "twitch" {
		parsed, err = o.updateTwitchURL(parsedUrl.Host)
		if err != nil {
			return nil, err
		}
	} else if match := twitchEndpoint.FindStringSubmatch(rawUrl); len(match) > 0 {
		parsed, err = o.updateTwitchURL(match[1])
		if err != nil {
			return nil, err
		}
	} else {
		parsed = rawUrl
	}

	stream, ok := o.Streams.Load(parsed)
	if !ok {
		return nil, errors.ErrStreamNotFound(rawUrl)
	}
	return stream.(*Stream), nil
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

func redactStreamKey(url string) (string, string, bool) {
	match := rtmpRegexp.FindStringSubmatch(url)
	if len(match) != 6 {
		return url, "", false
	}

	streamID := match[4]
	match[4] = utils.RedactIdentifier(match[4])
	return strings.Join(match[1:], ""), streamID, true
}
