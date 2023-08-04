// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

type uploader interface {
	upload(string, string, types.OutputType) (string, int64, error)
}

func New(conf config.UploadConfig, backup string) (Uploader, error) {
	var u uploader
	var err error

	switch c := conf.(type) {
	case *livekit.S3Upload:
		u, err = newS3Uploader(c)
	case *livekit.GCPUpload:
		u, err = newGCPUploader(c)
	case *livekit.AzureBlobUpload:
		u, err = newAzureUploader(c)
	case *livekit.AliOSSUpload:
		u, err = newAliOSSUploader(c)
	default:
		return &localUploader{}, nil
	}
	if err != nil {
		return nil, err
	}

	return &remoteUploader{
		uploader: u,
		backup:   backup,
	}, nil
}

type remoteUploader struct {
	uploader

	backup string
}

func (u *remoteUploader) Upload(localFilepath, storageFilepath string, outputType types.OutputType, deleteAfterUpload bool) (string, int64, error) {
	location, size, err := u.upload(localFilepath, storageFilepath, outputType)
	if err == nil {
		if deleteAfterUpload {
			_ = os.Remove(localFilepath)
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

type localUploader struct{}

func (u *localUploader) Upload(localFilepath, _ string, _ types.OutputType, _ bool) (string, int64, error) {
	stat, err := os.Stat(localFilepath)
	if err != nil {
		return "", 0, err
	}

	return localFilepath, stat.Size(), nil
}

func wrap(name string, err error) error {
	return errors.Wrap(err, fmt.Sprintf("%s upload failed", name))
}
