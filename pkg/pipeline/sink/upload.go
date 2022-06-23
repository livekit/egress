package sink

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-storage-blob-go/azblob"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/egress/pkg/pipeline/params"
)

const (
	maxRetries = 5
	minDelay   = 100 * time.Millisecond
	maxDelay   = 5 * time.Second
)

// FIXME Should we use a Context to allow for an overall operation timeout?

func UploadS3(conf *livekit.S3Upload, localFilePath, requestedPath string, mime params.OutputType) (location string, err error) {
	sess, err := session.NewSession(&aws.Config{
		Credentials: credentials.NewStaticCredentials(conf.AccessKey, conf.Secret, ""),
		Endpoint:    aws.String(conf.Endpoint),
		Region:      aws.String(conf.Region),
		MaxRetries:  aws.Int(maxRetries), // Switching to v2 of the aws Go SDK would allow to set a maxDelay as well.
	})
	if err != nil {
		return "", err
	}

	file, err := os.Open(localFilePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return "", err
	}

	_, err = s3.New(sess).PutObject(&s3.PutObjectInput{
		Bucket:        aws.String(conf.Bucket),
		Key:           aws.String(requestedPath),
		Body:          file,
		ContentLength: aws.Int64(fileInfo.Size()),
		ContentType:   aws.String(string(mime)),
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", conf.Bucket, conf.Region, requestedPath), nil
}

func UploadAzure(conf *livekit.AzureBlobUpload, localFilePath, requestedPath string, mime params.OutputType) (location string, err error) {
	credential, err := azblob.NewSharedKeyCredential(
		conf.AccountName,
		conf.AccountKey,
	)
	if err != nil {
		return "", err
	}

	pipeline := azblob.NewPipeline(credential, azblob.PipelineOptions{
		Retry: azblob.RetryOptions{
			Policy:        azblob.RetryPolicyExponential,
			MaxTries:      maxRetries,
			RetryDelay:    minDelay,
			MaxRetryDelay: maxDelay,
		},
	})
	sUrl := fmt.Sprintf("https://%s.blob.core.windows.net/%s", conf.AccountName, conf.ContainerName)
	azUrl, err := url.Parse(sUrl)
	if err != nil {
		return "", err
	}

	containerURL := azblob.NewContainerURL(*azUrl, pipeline)
	blobURL := containerURL.NewBlockBlobURL(requestedPath)

	file, err := os.Open(localFilePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// upload blocks in parallel for optimal performance
	// it calls PutBlock/PutBlockList for files larger than 256 MBs and PutBlob for smaller files
	_, err = azblob.UploadFileToBlockBlob(context.Background(), file, blobURL, azblob.UploadToBlockBlobOptions{
		BlobHTTPHeaders: azblob.BlobHTTPHeaders{ContentType: string(mime)},
		BlockSize:       4 * 1024 * 1024,
		Parallelism:     16,
	})
	if err != nil {
		return "", err
	}

	return sUrl, nil
}

func UploadGCP(conf *livekit.GCPUpload, localFilePath, requestedPath string, mime params.OutputType) (location string, err error) {
	ctx := context.Background()
	var client *storage.Client

	if conf.Credentials != nil {
		client, err = storage.NewClient(ctx, option.WithCredentialsJSON(conf.Credentials))
	} else {
		client, err = storage.NewClient(ctx)
	}
	if err != nil {
		return "", err
	}
	defer client.Close()

	file, err := os.Open(localFilePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// In case where the total amount of data to upload is larger than googleapi.DefaultUploadChunkSize, each upload request will have a timeout of
	// ChunkRetryDeadline, which is 32s by default. If the request payload is smaller than googleapi.DefaultUploadChunkSize, use a context deadline
	// to apply the same timeouit
	fileInfo, err := file.Stat()
	if err != nil {
		return "", err
	}

	var wctx context.Context
	if fileInfo.Size() <= googleapi.DefaultUploadChunkSize {
		var cancel context.CancelFunc
		wctx, cancel = context.WithTimeout(ctx, 32*time.Second)
		defer cancel()
	} else {
		wctx = ctx
	}

	wc := client.Bucket(conf.Bucket).Object(requestedPath).Retryer(storage.WithBackoff(gax.Backoff{
		Initial:    minDelay,
		Max:        maxDelay,
		Multiplier: 2,
	}),
		storage.WithPolicy(storage.RetryAlways),
	).NewWriter(wctx)

	if _, err = io.Copy(wc, file); err != nil {
		return "", err
	}

	if err = wc.Close(); err != nil {
		return "", err
	}

	return fmt.Sprintf("https://%s.storage.googleapis.com/%s", conf.Bucket, requestedPath), nil
}
