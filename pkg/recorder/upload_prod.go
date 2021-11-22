// +build !test

package recorder

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/Azure/azure-storage-blob-go/azblob"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

// TODO: write to persistent volume, use separate upload process

func (r *Recorder) uploadS3() error {
	sess, err := session.NewSession(&aws.Config{
		Credentials: credentials.NewStaticCredentials(
			r.conf.FileOutput.S3.AccessKey,
			r.conf.FileOutput.S3.Secret,
			"",
		),
		Endpoint: aws.String(r.conf.FileOutput.S3.Endpoint),
		Region:   aws.String(r.conf.FileOutput.S3.Region),
	})
	if err != nil {
		return err
	}

	file, err := os.Open(r.filename)
	if err != nil {
		return err
	}
	defer file.Close()

	fileInfo, _ := file.Stat()
	size := fileInfo.Size()
	buffer := make([]byte, size)
	if _, err = file.Read(buffer); err != nil {
		return err
	}

	_, err = s3.New(sess).PutObject(&s3.PutObjectInput{
		Bucket:        aws.String(r.conf.FileOutput.S3.Bucket),
		Key:           aws.String(r.filepath),
		Body:          bytes.NewReader(buffer),
		ContentLength: aws.Int64(size),
		ContentType:   aws.String("video/mp4"),
	})

	return err
}

func (r *Recorder) uploadAzure() error {
	credential, err := azblob.NewSharedKeyCredential(
		r.conf.FileOutput.Azblob.AccountName,
		r.conf.FileOutput.Azblob.AccountKey,
	)
	if err != nil {
		return err
	}

	p := azblob.NewPipeline(credential, azblob.PipelineOptions{})

	URL, _ := url.Parse(
		fmt.Sprintf("https://%s.blob.core.windows.net/%s",
			r.conf.FileOutput.Azblob.AccountName,
			r.conf.FileOutput.Azblob.ContainerName,
		),
	)

	containerURL := azblob.NewContainerURL(*URL, p)

	blobURL := containerURL.NewBlockBlobURL(r.filepath)
	file, err := os.Open(r.filename)
	if err != nil {
		return err
	}
	// upload blocks in parallel for optimal performance
	// it calls PutBlock/PutBlockList for files larger than 256 MBs and PutBlob for smaller files
	ctx := context.Background()
	_, err = azblob.UploadFileToBlockBlob(ctx, file, blobURL, azblob.UploadToBlockBlobOptions{
		BlobHTTPHeaders: azblob.BlobHTTPHeaders{ContentType: "video/mp4"},
		BlockSize:       4 * 1024 * 1024,
		Parallelism:     16,
	})
	return err
}
