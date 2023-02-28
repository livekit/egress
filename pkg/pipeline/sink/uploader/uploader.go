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

type Uploader interface {
	Upload(string, string, types.OutputType) (string, int64, error)
}

func New(conf interface{}) (Uploader, error) {
	switch c := conf.(type) {
	case *livekit.S3Upload:
		return newS3Uploader(c)
	case *livekit.GCPUpload:
		return newGCPUploader(c)
	case *livekit.AzureBlobUpload:
		return newAzureUploader(c)
	case *livekit.AliOSSUpload:
		return newAliOSSUploader(c)
	default:
		return &noOpUploader{}, nil
	}
}

type noOpUploader struct{}

func (u *noOpUploader) Upload(localFilepath, _ string, _ types.OutputType) (string, int64, error) {
	stat, err := os.Stat(localFilepath)
	if err != nil {
		return "", 0, err
	}

	return localFilepath, stat.Size(), nil
}
