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
	"net/url"
	"os"
	"path"

	"github.com/Azure/azure-storage-blob-go/azblob"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
)

type AzureUploader struct {
	conf      *config.AzureConfig
	prefix    string
	container string
}

func newAzureUploader(conf *config.AzureConfig, prefix string) (uploader, error) {
	return &AzureUploader{
		conf:      conf,
		prefix:    prefix,
		container: fmt.Sprintf("https://%s.blob.core.windows.net/%s", conf.AccountName, conf.ContainerName),
	}, nil
}

func (u *AzureUploader) upload(localFilepath, storageFilepath string, outputType types.OutputType) (string, int64, error) {
	storageFilepath = path.Join(u.prefix, storageFilepath)

	credential, err := azblob.NewSharedKeyCredential(
		u.conf.AccountName,
		u.conf.AccountKey,
	)
	if err != nil {
		return "", 0, errors.ErrUploadFailed("Azure", err)
	}

	azUrl, err := url.Parse(u.container)
	if err != nil {
		return "", 0, errors.ErrUploadFailed("Azure", err)
	}

	pipeline := azblob.NewPipeline(credential, azblob.PipelineOptions{
		Retry: azblob.RetryOptions{
			Policy:        azblob.RetryPolicyExponential,
			MaxTries:      maxRetries,
			RetryDelay:    minDelay,
			MaxRetryDelay: maxDelay,
		},
	})
	containerURL := azblob.NewContainerURL(*azUrl, pipeline)
	blobURL := containerURL.NewBlockBlobURL(storageFilepath)

	file, err := os.Open(localFilepath)
	if err != nil {
		return "", 0, errors.ErrUploadFailed("Azure", err)
	}
	defer func() {
		_ = file.Close()
	}()

	stat, err := file.Stat()
	if err != nil {
		return "", 0, errors.ErrUploadFailed("Azure", err)
	}

	// upload blocks in parallel for optimal performance
	// it calls PutBlock/PutBlockList for files larger than 256 MBs and PutBlob for smaller files
	_, err = azblob.UploadFileToBlockBlob(context.Background(), file, blobURL, azblob.UploadToBlockBlobOptions{
		BlobHTTPHeaders: azblob.BlobHTTPHeaders{ContentType: string(outputType)},
		BlockSize:       4 * 1024 * 1024,
		Parallelism:     16,
	})
	if err != nil {
		return "", 0, errors.ErrUploadFailed("Azure", err)
	}

	return fmt.Sprintf("%s/%s", u.container, storageFilepath), stat.Size(), nil
}
