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
	*baseUploader

	conf *livekit.GCPUpload
}

func newGCPUploader(conf *livekit.GCPUpload, backup string) (Uploader, error) {
	u := &GCPUploader{
		conf: conf,
	}

	u.baseUploader = newBaseUploader(backup, u.upload)

	return u, nil
}

func (u *GCPUploader) upload(localFilepath, storageFilepath string, _ types.OutputType) (string, int64, error) {
	ctx := context.Background()

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

	var client *storage.Client
	if u.conf.Credentials != "" {
		client, err = storage.NewClient(ctx, option.WithCredentialsJSON([]byte(u.conf.Credentials)))
	} else {
		client, err = storage.NewClient(ctx)
	}
	if err != nil {
		return "", 0, wrap("GCP", err)
	}
	defer func() {
		_ = client.Close()
	}()

	// In case where the total amount of data to upload is larger than googleapi.DefaultUploadChunkSize, each upload request will have a timeout of
	// ChunkRetryDeadline, which is 32s by default. If the request payload is smaller than googleapi.DefaultUploadChunkSize, use a context deadline
	// to apply the same timeout
	var wctx context.Context
	if stat.Size() <= googleapi.DefaultUploadChunkSize {
		var cancel context.CancelFunc
		wctx, cancel = context.WithTimeout(ctx, time.Second*32)
		defer cancel()
	} else {
		wctx = ctx
	}

	wc := client.Bucket(u.conf.Bucket).Object(storageFilepath).Retryer(
		storage.WithBackoff(gax.Backoff{
			Initial:    minDelay,
			Max:        maxDelay,
			Multiplier: 2,
		}),
		storage.WithPolicy(storage.RetryAlways),
	).NewWriter(wctx)

	if _, err = io.Copy(wc, file); err != nil {
		return "", 0, wrap("GCP", err)
	}

	if err = wc.Close(); err != nil {
		return "", 0, wrap("GCP", err)
	}

	return fmt.Sprintf("https://%s.storage.googleapis.com/%s", u.conf.Bucket, storageFilepath), stat.Size(), nil
}
