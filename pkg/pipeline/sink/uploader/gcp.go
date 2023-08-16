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
	"os"
	"time"

	"cloud.google.com/go/storage"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

type GCPUploader struct {
	conf   *livekit.GCPUpload
	client *storage.Client
}

func newGCPUploader(conf *livekit.GCPUpload) (uploader, error) {
	u := &GCPUploader{
		conf: conf,
	}

	var err error
	if conf.Credentials != "" {
		u.client, err = storage.NewClient(context.Background(), option.WithCredentialsJSON([]byte(u.conf.Credentials)))
	} else {
		u.client, err = storage.NewClient(context.Background())
	}
	if err != nil {
		return nil, err
	}

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
		ctx, cancel = context.WithTimeout(context.Background(), time.Second*32)
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

	if _, err = io.Copy(wc, file); err != nil {
		return "", 0, wrap("GCP", err)
	}

	if err = wc.Close(); err != nil {
		return "", 0, wrap("GCP", err)
	}

	return fmt.Sprintf("https://%s.storage.googleapis.com/%s", u.conf.Bucket, storageFilepath), stat.Size(), nil
}
