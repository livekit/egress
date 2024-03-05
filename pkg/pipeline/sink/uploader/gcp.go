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
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"cloud.google.com/go/storage"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

const gcpTimeout = time.Minute

type GCPUploader struct {
	conf   *livekit.GCPUpload
	client *storage.Client
}

func newGCPUploader(conf *livekit.GCPUpload) (uploader, error) {
	u := &GCPUploader{
		conf: conf,
	}

	var opts []option.ClientOption
	if conf.Credentials != "" {
		opts = append(opts, option.WithCredentialsJSON([]byte(conf.Credentials)))
	}

	// override default transport DialContext
	defaultTransport := http.DefaultTransport.(*http.Transport).Clone()
	http.DefaultTransport.(*http.Transport).DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{
			Timeout:       time.Second * 30,
			KeepAlive:     time.Second * 30,
			FallbackDelay: -1,
			ControlContext: func(ctx context.Context, network, address string, c syscall.RawConn) error {
				// force ipv4 to avoid "service not available in your location, forbidden" errors from Google
				if network == "tcp6" {
					return errors.New("tcp6 disabled")
				}
				return nil
			},
		}).DialContext(ctx, network, addr)
	}

	c, err := storage.NewClient(context.Background(), opts...)
	// restore default transport
	http.DefaultTransport = defaultTransport
	if err != nil {
		return nil, err
	}
	u.client = c

	return u, nil
}

func (u *GCPUploader) upload(localFilepath, storageFilepath string, _ types.OutputType) (string, int64, error) {
	file, err := os.Open(localFilepath)
	if err != nil {
		return "", 0, wrap("GCP", err)
	}
	defer func() {
		_ = file.Close()
	}()

	stat, err := file.Stat()
	if err != nil {
		return "", 0, wrap("GCP", err)
	}

	// In case where the total amount of data to upload is larger than googleapi.DefaultUploadChunkSize, each upload request will have a timeout of
	// ChunkRetryDeadline, which is 32s by default. If the request payload is smaller than googleapi.DefaultUploadChunkSize, use a context deadline
	// to apply the same timeout
	var ctx context.Context
	if stat.Size() <= googleapi.DefaultUploadChunkSize {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), gcpTimeout)
		defer cancel()
	} else {
		ctx = context.Background()
	}

	wc := u.client.Bucket(u.conf.Bucket).Object(storageFilepath).Retryer(
		storage.WithBackoff(gax.Backoff{
			Initial:    minDelay,
			Max:        maxDelay,
			Multiplier: 2,
		}),
		storage.WithPolicy(storage.RetryAlways),
	).NewWriter(ctx)
	wc.ChunkRetryDeadline = gcpTimeout

	if _, err = io.Copy(wc, file); err != nil {
		return "", 0, wrap("GCP", err)
	}

	if err = wc.Close(); err != nil {
		return "", 0, wrap("GCP", err)
	}

	return fmt.Sprintf("https://%s.storage.googleapis.com/%s", u.conf.Bucket, storageFilepath), stat.Size(), nil
}
