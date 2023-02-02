package uploader

import (
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

func New(conf interface{}) Uploader {
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
		return nil
	}
}
