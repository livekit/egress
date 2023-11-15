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

//go:build integration

package test

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-storage-blob-go/azblob"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/googleapis/gax-go/v2"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

func download(t *testing.T, uploadParams interface{}, localFilepath, storageFilepath string) {
	switch u := uploadParams.(type) {
	case *config.EgressS3Upload:
		logger.Debugw("s3 download", "localFilepath", localFilepath, "storageFilepath", storageFilepath)
		downloadS3(t, u, localFilepath, storageFilepath)

	case *livekit.GCPUpload:
		logger.Debugw("gcp download", "localFilepath", localFilepath, "storageFilepath", storageFilepath)
		downloadGCP(t, u, localFilepath, storageFilepath)

	case *livekit.AzureBlobUpload:
		logger.Debugw("azure download", "localFilepath", localFilepath, "storageFilepath", storageFilepath)
		downloadAzure(t, u, localFilepath, storageFilepath)
	}
}

func downloadS3(t *testing.T, conf *config.EgressS3Upload, localFilepath, storageFilepath string) {
	sess, err := session.NewSession(&aws.Config{
		Credentials: credentials.NewStaticCredentials(conf.AccessKey, conf.Secret, ""),
		Endpoint:    aws.String(conf.Endpoint),
		Region:      aws.String(conf.Region),
		MaxRetries:  aws.Int(maxRetries),
	})
	require.NoError(t, err)

	file, err := os.Create(localFilepath)
	require.NoError(t, err)
	defer file.Close()

	_, err = s3manager.NewDownloader(sess).Download(file,
		&s3.GetObjectInput{
			Bucket: aws.String(conf.Bucket),
			Key:    aws.String(storageFilepath),
		},
	)
	require.NoError(t, err)

	_, err = s3.New(sess).DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(conf.Bucket),
		Key:    aws.String(storageFilepath),
	})
	require.NoError(t, err)
}

func downloadAzure(t *testing.T, conf *livekit.AzureBlobUpload, localFilepath, storageFilepath string) {
	credential, err := azblob.NewSharedKeyCredential(
		conf.AccountName,
		conf.AccountKey,
	)
	require.NoError(t, err)

	pipeline := azblob.NewPipeline(credential, azblob.PipelineOptions{
		Retry: azblob.RetryOptions{
			Policy:        azblob.RetryPolicyExponential,
			MaxTries:      maxRetries,
			MaxRetryDelay: maxDelay,
		},
	})
	sUrl := fmt.Sprintf("https://%s.blob.core.windows.net/%s", conf.AccountName, conf.ContainerName)
	azUrl, err := url.Parse(sUrl)
	require.NoError(t, err)

	containerURL := azblob.NewContainerURL(*azUrl, pipeline)
	blobURL := containerURL.NewBlobURL(storageFilepath)

	file, err := os.Create(localFilepath)
	require.NoError(t, err)
	defer file.Close()

	err = azblob.DownloadBlobToFile(context.Background(), blobURL, 0, 0, file, azblob.DownloadFromBlobOptions{
		BlockSize:   4 * 1024 * 1024,
		Parallelism: 16,
		RetryReaderOptionsPerBlock: azblob.RetryReaderOptions{
			MaxRetryRequests: 3,
		},
	})
	require.NoError(t, err)

	_, err = blobURL.Delete(context.Background(), azblob.DeleteSnapshotsOptionNone, azblob.BlobAccessConditions{})
	require.NoError(t, err)
}

func downloadGCP(t *testing.T, conf *livekit.GCPUpload, localFilepath, storageFilepath string) {
	ctx := context.Background()
	var client *storage.Client

	var err error
	if conf.Credentials != "" {
		client, err = storage.NewClient(ctx, option.WithCredentialsJSON([]byte(conf.Credentials)))
	} else {
		client, err = storage.NewClient(ctx)
	}
	require.NoError(t, err)
	defer client.Close()

	file, err := os.Create(localFilepath)
	require.NoError(t, err)
	defer file.Close()

	rc, err := client.Bucket(conf.Bucket).Object(storageFilepath).Retryer(
		storage.WithBackoff(
			gax.Backoff{
				Initial:    minDelay,
				Max:        maxDelay,
				Multiplier: 2,
			}),
		storage.WithPolicy(storage.RetryAlways),
	).NewReader(ctx)
	require.NoError(t, err)

	_, err = io.Copy(file, rc)
	_ = rc.Close()
	require.NoError(t, err)

	err = client.Bucket(conf.Bucket).Object(storageFilepath).Delete(context.Background())
	require.NoError(t, err)
}
