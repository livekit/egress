package uploader

import (
	"os"
	"time"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

const (
	maxRetries = 5
	minDelay   = time.Millisecond * 100
	maxDelay   = time.Second * 5
)

type Uploader struct {
	uploader
}

type uploader interface {
	upload(string, string, types.OutputType) (string, int64, error)
}

func New(conf interface{}) (*Uploader, error) {
	u := &Uploader{}

	var i uploader
	var err error
	switch c := conf.(type) {
	case *livekit.S3Upload:
		i, err = newS3Uploader(c)
	case *livekit.GCPUpload:
		i, err = newGCPUploader(c)
	case *livekit.AzureBlobUpload:
		i, err = newAzureUploader(c)
	case *livekit.AliOSSUpload:
		i, err = newAliOSSUploader(c)
	default:
		i = &noOpUploader{}
	}
	if err != nil {
		return nil, err
	}

	u.uploader = i
	return u, nil
}

func (u *Uploader) Upload(localFilepath, storageFilepath string, outputType types.OutputType) (string, int64, error) {
	location, size, err := u.upload(localFilepath, storageFilepath, outputType)
	if err != nil {
		// TODO: config option for backup location
		return "", 0, nil
	}

	return location, size, nil
}

type noOpUploader struct{}

func (u *noOpUploader) upload(localFilepath, _ string, _ types.OutputType) (string, int64, error) {
	stat, err := os.Stat(localFilepath)
	if err != nil {
		return "", 0, err
	}

	return localFilepath, stat.Size(), nil
}
