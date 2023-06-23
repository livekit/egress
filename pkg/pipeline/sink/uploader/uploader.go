package uploader

import (
	"fmt"
	"os"
	"path"
	"time"

	"github.com/pkg/errors"

	"github.com/livekit/egress/pkg/config"
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
	backup string
}

type uploader interface {
	upload(string, string, types.OutputType) (string, int64, error)
}

func New(conf config.UploadConfig, backup string) (*Uploader, error) {
	u := &Uploader{
		backup: backup,
	}

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
	if err == nil {
		return location, size, nil
	}

	if u.backup != "" {
		stat, err := os.Stat(localFilepath)
		if err != nil {
			return "", 0, err
		}

		backupFilepath := path.Join(u.backup, storageFilepath)
		if err = os.Rename(localFilepath, backupFilepath); err != nil {
			return "", 0, err
		}

		return backupFilepath, stat.Size(), nil
	}

	return "", 0, err
}

type noOpUploader struct{}

func (u *noOpUploader) upload(localFilepath, _ string, _ types.OutputType) (string, int64, error) {
	stat, err := os.Stat(localFilepath)
	if err != nil {
		return "", 0, err
	}

	return localFilepath, stat.Size(), nil
}

func wrap(name string, err error) error {
	return errors.Wrap(err, fmt.Sprintf("%s upload failed", name))
}
