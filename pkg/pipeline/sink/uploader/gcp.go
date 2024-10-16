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

package uploader

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"

	"cloud.google.com/go/storage"
	"github.com/googleapis/gax-go/v2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
)

const storageScope = "https://www.googleapis.com/auth/devstorage.read_write"

type GCPUploader struct {
	conf   *config.GCPConfig
	prefix string
	client *storage.Client
}

func newGCPUploader(conf *config.GCPConfig, prefix string) (uploader, error) {
	u := &GCPUploader{
		conf:   conf,
		prefix: prefix,
	}

	var opts []option.ClientOption
	if conf.CredentialsJSON != "" {
		jwtConfig, err := google.JWTConfigFromJSON([]byte(conf.CredentialsJSON), storageScope)
		if err != nil {
			return nil, err
		}
		opts = append(opts, option.WithTokenSource(jwtConfig.TokenSource(context.Background())))
	}

	defaultTransport := http.DefaultTransport.(*http.Transport)
	transportClone := defaultTransport.Clone()

	if conf.ProxyConfig != nil {
		proxyUrl, err := url.Parse(conf.ProxyConfig.Url)
		if err != nil {
			return nil, err
		}
		defaultTransport.Proxy = http.ProxyURL(proxyUrl)
		if conf.ProxyConfig.Username != "" && conf.ProxyConfig.Password != "" {
			auth := fmt.Sprintf("%s:%s", conf.ProxyConfig.Username, conf.ProxyConfig.Password)
			basicAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(auth))
			defaultTransport.ProxyConnectHeader = http.Header{}
			defaultTransport.ProxyConnectHeader.Add("Proxy-Authorization", basicAuth)
		}
	}
	c, err := storage.NewClient(context.Background(), opts...)

	// restore default transport
	http.DefaultTransport = transportClone
	if err != nil {
		return nil, err
	}

	u.client = c
	return u, nil
}

func (u *GCPUploader) upload(localFilepath, storageFilepath string, _ types.OutputType) (string, int64, error) {
	storageFilepath = path.Join(u.prefix, storageFilepath)

	file, err := os.Open(localFilepath)
	if err != nil {
		return "", 0, errors.ErrUploadFailed("GCP", err)
	}
	defer func() {
		_ = file.Close()
	}()

	stat, err := file.Stat()
	if err != nil {
		return "", 0, errors.ErrUploadFailed("GCP", err)
	}

	wc := u.client.Bucket(u.conf.Bucket).Object(storageFilepath).Retryer(
		storage.WithBackoff(gax.Backoff{
			Initial:    minDelay,
			Max:        maxDelay,
			Multiplier: 2,
		}),
		storage.WithMaxAttempts(maxRetries),
		storage.WithPolicy(storage.RetryAlways),
	).NewWriter(context.Background())
	wc.ChunkRetryDeadline = 0

	if _, err = io.Copy(wc, file); err != nil {
		return "", 0, errors.ErrUploadFailed("GCP", err)
	}

	if err = wc.Close(); err != nil {
		return "", 0, errors.ErrUploadFailed("GCP", err)
	}

	return fmt.Sprintf("https://%s.storage.googleapis.com/%s", u.conf.Bucket, storageFilepath), stat.Size(), nil
}
