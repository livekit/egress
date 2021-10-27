// +build !test

package recorder

import (
	"bytes"
	"os"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

// TODO: write to persistent volume, use separate upload process
func (r *Recorder) upload() error {
	sess, err := session.NewSession(&aws.Config{
		Credentials: credentials.NewStaticCredentials(
			r.conf.FileOutput.S3.AccessKey,
			r.conf.FileOutput.S3.Secret,
			"",
		),
		Region: aws.String(r.conf.FileOutput.S3.Region),
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
