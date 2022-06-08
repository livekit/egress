package sink

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-storage-blob-go/azblob"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"google.golang.org/api/option"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/egress/pkg/pipeline/params"
)

const maxRetries = 5

func UploadS3(conf *livekit.S3Upload, localFilePath, requestedPath string, mime params.OutputType) (location string, err error) {
	for i := 0; i < maxRetries; i++ {
		location, err = uploadS3(conf, localFilePath, requestedPath, mime)
		if err == nil {
			return
		}
	}
	return
}

func uploadS3(conf *livekit.S3Upload, localFilePath, requestedPath string, mime params.OutputType) (string, error) {
	sess, err := session.NewSession(&aws.Config{
		Credentials: credentials.NewStaticCredentials(conf.AccessKey, conf.Secret, ""),
		Endpoint:    aws.String(conf.Endpoint),
		Region:      aws.String(conf.Region),
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
	for i := 0; i < maxRetries; i++ {
		location, err = uploadAzure(conf, localFilePath, requestedPath, mime)
		if err == nil {
			return
		}
	}
	return
}

func uploadAzure(conf *livekit.AzureBlobUpload, localFilePath, requestedPath string, mime params.OutputType) (string, error) {
	credential, err := azblob.NewSharedKeyCredential(
		conf.AccountName,
		conf.AccountKey,
	)
	if err != nil {
		return "", err
	}

	pipeline := azblob.NewPipeline(credential, azblob.PipelineOptions{})
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
	for i := 0; i < maxRetries; i++ {
		location, err = uploadGCP(conf, localFilePath, requestedPath, mime)
		if err == nil {
			return
		}
	}
	return
}

func uploadGCP(conf *livekit.GCPUpload, localFilePath, requestedPath string, mime params.OutputType) (string, error) {
	ctx := context.Background()
	var client *storage.Client
	var err error

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

	wc := client.Bucket(conf.Bucket).Object(requestedPath).NewWriter(ctx)
	if _, err = io.Copy(wc, file); err != nil {
		return "", err
	}

	if err = wc.Close(); err != nil {
		return "", err
	}

	return fmt.Sprintf("https://%s.storage.googleapis.com/%s", conf.Bucket, requestedPath), nil
}
