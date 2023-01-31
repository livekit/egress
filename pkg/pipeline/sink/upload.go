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
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

const (
	maxRetries = 5
	minDelay   = time.Millisecond * 100
	maxDelay   = time.Second * 5
)

func UploadS3(conf *livekit.S3Upload, localFilepath, storageFilepath string, mime types.OutputType) (location string, err error) {
	awsConfig := &aws.Config{
		MaxRetries:       aws.Int(maxRetries), // Switching to v2 of the aws Go SDK would allow to set a maxDelay as well.
		S3ForcePathStyle: aws.Bool(conf.ForcePathStyle),
	}
	if conf.AccessKey != "" && conf.Secret != "" {
		awsConfig.Credentials = credentials.NewStaticCredentials(conf.AccessKey, conf.Secret, "")
	}
	if conf.Endpoint != "" {
		awsConfig.Endpoint = aws.String(conf.Endpoint)
	}
	if conf.Region != "" {
		awsConfig.Region = aws.String(conf.Region)
	}

	sess, err := session.NewSession(awsConfig)
	if err != nil {
		return "", err
	}

	file, err := os.Open(localFilepath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	_, err = s3manager.NewUploader(sess).Upload(&s3manager.UploadInput{
		Body:        file,
		Bucket:      aws.String(conf.Bucket),
		ContentType: aws.String(string(mime)),
		Key:         aws.String(storageFilepath),
		Metadata:    getS3Metadata(conf.Metadata),
		Tagging:     getS3Tagging(conf.Tagging),
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("https://%s.s3.amazonaws.com/%s", conf.Bucket, storageFilepath), nil
}

func getS3Metadata(metadata map[string]string) map[string]*string {
	if len(metadata) == 0 {
		return nil
	}

	result := make(map[string]*string)
	for k, v := range metadata {
		val := v
		result[k] = &val
	}
	return result
}

func getS3Tagging(tagging string) *string {
	if tagging != "" {
		return aws.String(tagging)
	}
	return nil
}

func UploadAzure(conf *livekit.AzureBlobUpload, localFilepath, storageFilepath string, mime types.OutputType) (location string, err error) {
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
	blobURL := containerURL.NewBlockBlobURL(storageFilepath)

	file, err := os.Open(localFilepath)
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

func UploadGCP(conf *livekit.GCPUpload, localFilepath, storageFilepath string) (location string, err error) {
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

	file, err := os.Open(localFilepath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// In case where the total amount of data to upload is larger than googleapi.DefaultUploadChunkSize, each upload request will have a timeout of
	// ChunkRetryDeadline, which is 32s by default. If the request payload is smaller than googleapi.DefaultUploadChunkSize, use a context deadline
	// to apply the same timeout
	fileInfo, err := file.Stat()
	if err != nil {
		return "", err
	}

	var wctx context.Context
	if fileInfo.Size() <= googleapi.DefaultUploadChunkSize {
		var cancel context.CancelFunc
		wctx, cancel = context.WithTimeout(ctx, time.Second*32)
		defer cancel()
	} else {
		wctx = ctx
	}

	wc := client.Bucket(conf.Bucket).Object(storageFilepath).Retryer(storage.WithBackoff(gax.Backoff{
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

	return fmt.Sprintf("https://%s.storage.googleapis.com/%s", conf.Bucket, storageFilepath), nil
}

func UploadAliOSS(conf *livekit.AliOSSUpload, localFilePath, requestedPath string) (location string, err error) {
	client, err := oss.New(conf.Endpoint, conf.AccessKey, conf.Secret)
	if err != nil {
		return "", err
	}
	bucket, err := client.Bucket(conf.Bucket)
	if err != nil {
		return "", err
	}
	err = bucket.PutObjectFromFile(requestedPath, localFilePath)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%s.%s/%s", conf.Bucket, conf.Endpoint, requestedPath), nil
}
