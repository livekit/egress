package uploader

import (
	"fmt"
	"os"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

type AliOSSUploader struct {
	conf *livekit.AliOSSUpload
}

func newAliOSSUploader(conf *livekit.AliOSSUpload) (Uploader, error) {
	return &AliOSSUploader{
		conf: conf,
	}, nil
}

func (u *AliOSSUploader) Upload(localFilePath, requestedPath string, _ types.OutputType) (string, int64, error) {
	stat, err := os.Stat(localFilePath)
	if err != nil {
		return "", 0, err
	}

	client, err := oss.New(u.conf.Endpoint, u.conf.AccessKey, u.conf.Secret)
	if err != nil {
		return "", 0, err
	}

	bucket, err := client.Bucket(u.conf.Bucket)
	if err != nil {
		return "", 0, err
	}

	err = bucket.PutObjectFromFile(requestedPath, localFilePath)
	if err != nil {
		return "", 0, err
	}

	return fmt.Sprintf("https://%s.%s/%s", u.conf.Bucket, u.conf.Endpoint, requestedPath), stat.Size(), nil
}
