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

	"github.com/livekit/livekit-egress/pkg/pipeline/params"
)

func UploadS3(conf *livekit.S3Upload, p *params.Params) (string, error) {
	sess, err := session.NewSession(&aws.Config{
		Credentials: credentials.NewStaticCredentials(conf.AccessKey, conf.Secret, ""),
		Endpoint:    aws.String(conf.Endpoint),
		Region:      aws.String(conf.Region),
	})
	if err != nil {
		return "", err
	}

	file, err := os.Open(p.Filename)
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
		Key:           aws.String(p.Filepath),
		Body:          file,
		ContentLength: aws.Int64(fileInfo.Size()),
		ContentType:   aws.String(string(p.OutputType)),
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", conf.Bucket, conf.Region, p.Filepath), nil
}

func UploadAzure(conf *livekit.AzureBlobUpload, p *params.Params) (string, error) {
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
	blobURL := containerURL.NewBlockBlobURL(p.Filepath)

	file, err := os.Open(p.Filename)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// upload blocks in parallel for optimal performance
	// it calls PutBlock/PutBlockList for files larger than 256 MBs and PutBlob for smaller files
	_, err = azblob.UploadFileToBlockBlob(context.Background(), file, blobURL, azblob.UploadToBlockBlobOptions{
		BlobHTTPHeaders: azblob.BlobHTTPHeaders{ContentType: string(p.OutputType)},
		BlockSize:       4 * 1024 * 1024,
		Parallelism:     16,
	})
	if err != nil {
		return "", err
	}

	return sUrl, nil
}

func UploadGCP(conf *livekit.GCPUpload, p *params.Params) (string, error) {
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

	file, err := os.Open(p.Filename)
	if err != nil {
		return "", err
	}
	defer file.Close()

	wc := client.Bucket(conf.Bucket).Object(p.Filepath).NewWriter(ctx)
	if _, err = io.Copy(wc, file); err != nil {
		return "", err
	}

	if err = wc.Close(); err != nil {
		return "", err
	}

	return fmt.Sprintf("https://%s.storage.googleapis.com/%s", conf.Bucket, p.Filepath), nil
}
