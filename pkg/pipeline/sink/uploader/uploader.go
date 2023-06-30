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

type Uploader interface {
	Upload(string, string, types.OutputType, bool) (string, int64, error)
}

func New(conf config.UploadConfig, backup string) (Uploader, error) {
	var u Uploader

	var err error
	switch c := conf.(type) {
	case *livekit.S3Upload:
		u, err = newS3Uploader(c, backup)
	case *livekit.GCPUpload:
		u, err = newGCPUploader(c, backup)
	case *livekit.AzureBlobUpload:
		u, err = newAzureUploader(c, backup)
	case *livekit.AliOSSUpload:
		u, err = newAliOSSUploader(c, backup)
	default:
		u = &noOpUploader{}
	}
	if err != nil {
		return nil, err
	}

	return u, nil
}

type baseUploader struct {
	backup string
	upload func(localFilepath, storageFilepath string, outputType types.OutputType) (string, int64, error)
}

func newBaseUploader(backup string, upload func(localFilepath, storageFilepath string, outputType types.OutputType) (string, int64, error)) *baseUploader {
	return &baseUploader{
		backup: backup,
		upload: upload,
	}
}

func (u *baseUploader) Upload(localFilepath, storageFilepath string, outputType types.OutputType, deleteAfterUpload bool) (string, int64, error) {
	location, size, err := u.upload(localFilepath, storageFilepath, outputType)
	if err == nil {
		if deleteAfterUpload {
			os.Remove(localFilepath)
		}

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

func (u *noOpUploader) Upload(localFilepath, _ string, _ types.OutputType, deleteAfterUpload bool) (string, int64, error) {
	stat, err := os.Stat(localFilepath)
	if err != nil {
		return "", 0, err
	}

	return localFilepath, stat.Size(), nil
}

func (u *noOpUploader) cleanupFile(localFilepath string) error {
	return nil
}

func wrap(name string, err error) error {
	return errors.Wrap(err, fmt.Sprintf("%s upload failed", name))
}
