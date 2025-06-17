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
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-storage-blob-go/azblob"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsConfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/googleapis/gax-go/v2"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/protocol/logger"
	lkstorage "github.com/livekit/storage"
)

func loadManifest(t *testing.T, c *config.StorageConfig, localFilepath, storageFilepath string) *config.Manifest {
	download(t, c, localFilepath, storageFilepath, false)
	defer os.Remove(localFilepath)

	b, err := os.ReadFile(localFilepath)
	require.NoError(t, err)

	m := &config.Manifest{}
	err = json.Unmarshal(b, m)
	require.NoError(t, err)

	return m
}

func download(t *testing.T, c *config.StorageConfig, localFilepath, storageFilepath string, delete bool) {
	if c != nil {
		if c.S3 != nil {
			logger.Debugw("s3 download", "localFilepath", localFilepath, "storageFilepath", storageFilepath)
			downloadS3(t, c.S3, localFilepath, storageFilepath, delete)
		} else if c.GCP != nil {
			logger.Debugw("gcp download", "localFilepath", localFilepath, "storageFilepath", storageFilepath)
			downloadGCP(t, c.GCP, localFilepath, storageFilepath, delete)
		} else if c.Azure != nil {
			logger.Debugw("azure download", "localFilepath", localFilepath, "storageFilepath", storageFilepath)
			downloadAzure(t, c.Azure, localFilepath, storageFilepath, delete)
		}
	}
}

func downloadS3(t *testing.T, conf *lkstorage.S3Config, localFilepath, storageFilepath string, delete bool) {
	file, err := os.Create(localFilepath)
	require.NoError(t, err)
	defer file.Close()

	awsConf, err := awsConfig.LoadDefaultConfig(context.Background(), func(o *awsConfig.LoadOptions) error {
		o.Region = conf.Region
		o.Credentials = credentials.StaticCredentialsProvider{
			Value: aws.Credentials{
				AccessKeyID:     conf.AccessKey,
				SecretAccessKey: conf.Secret,
				SessionToken:    conf.SessionToken,
			},
		}

		return nil
	})
	require.NoError(t, err)
	s3Client := s3.NewFromConfig(awsConf)

	_, err = manager.NewDownloader(s3Client).Download(
		context.Background(),
		file,
		&s3.GetObjectInput{
			Bucket: aws.String(conf.Bucket),
			Key:    aws.String(storageFilepath),
		},
	)
	require.NoError(t, err)

	if delete {
		_, err = s3Client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
			Bucket: aws.String(conf.Bucket),
			Key:    aws.String(storageFilepath),
		})
		require.NoError(t, err)
	}
}

func downloadAzure(t *testing.T, conf *lkstorage.AzureConfig, localFilepath, storageFilepath string, delete bool) {
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

	if delete {
		_, err = blobURL.Delete(context.Background(), azblob.DeleteSnapshotsOptionNone, azblob.BlobAccessConditions{})
		require.NoError(t, err)
	}
}

func downloadGCP(t *testing.T, conf *lkstorage.GCPConfig, localFilepath, storageFilepath string, delete bool) {
	ctx := context.Background()
	var client *storage.Client

	var err error
	if conf.CredentialsJSON != "" {
		client, err = storage.NewClient(ctx, option.WithCredentialsJSON([]byte(conf.CredentialsJSON)))
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

	if delete {
		err = client.Bucket(conf.Bucket).Object(storageFilepath).Delete(context.Background())
		require.NoError(t, err)
	}
}
