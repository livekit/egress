// +build !test

package recorder

import (
	"bytes"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

// TODO: write to persistent volume, use separate upload process
func (r *Recorder) upload(s3Url string) error {
	sess, err := session.NewSession(&aws.Config{
		Credentials: credentials.NewStaticCredentials(
			r.conf.S3.AccessKey,
			r.conf.S3.Secret,
			"",
		),
		Region: aws.String(r.conf.S3.Region),
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

	var bucket, key string
	if strings.HasPrefix(s3Url, "s3://") {
		s3Url = s3Url[5:]
	}
	if idx := strings.Index(s3Url, "/"); idx != -1 {
		bucket = s3Url[:idx]
		key = s3Url[idx+1:]
	} else {
		bucket = s3Url
		key = "recording.mp4"
	}

	_, err = s3.New(sess).PutObject(&s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(buffer),
		ContentLength: aws.Int64(size),
		ContentType:   aws.String("video/mp4"),
	})

	return err
}
