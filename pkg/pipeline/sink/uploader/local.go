// Copyright 2024 LiveKit, Inc.
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
	"io"
	"os"
	"path"

	"github.com/livekit/egress/pkg/config"
)

type localUploader struct {
}

func newLocalUploader(c *config.StorageConfig) (*store, error) {
	return &store{
		Storage: &localUploader{},
		conf:    c,
		name:    "Local",
	}, nil
}

func (u *localUploader) UploadFile(localFilepath, storageFilepath string, _ string) (string, int64, error) {
	stat, err := os.Stat(localFilepath)
	if err != nil {
		return "", 0, err
	}

	dir, _ := path.Split(storageFilepath)
	if err = os.MkdirAll(dir, 0755); err != nil {
		return "", 0, err
	}

	local, err := os.Open(localFilepath)
	if err != nil {
		return "", 0, err
	}
	defer local.Close()

	storage, err := os.Create(storageFilepath)
	if err != nil {
		return "", 0, err
	}
	defer storage.Close()

	_, err = io.Copy(storage, local)
	if err != nil {
		return "", 0, err
	}

	return storageFilepath, stat.Size(), nil
}

func (u *localUploader) UploadData(data []byte, storagePath, contentType string) (location string, size int64, err error) {
	return "", 0, fmt.Errorf("Unimplemented")
}

func (u *localUploader) DownloadData(storagePath string) (data []byte, err error) {
	return nil, fmt.Errorf("Unimplemented")
}

func (u *localUploader) DownloadFile(filepath, storagePath string) (size int64, err error) {

	return 0, fmt.Errorf("Unimplemented")
}

func (u *localUploader) GeneratePresignedUrl(storagePath string) (url string, err error) {
	return "", fmt.Errorf("Unimplemented")
}

func (u *localUploader) Delete(storagePath string) error {
	return fmt.Errorf("Unimplemented")
}
