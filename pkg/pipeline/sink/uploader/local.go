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
	"io"
	"os"
	"path"

	"github.com/livekit/egress/pkg/types"
)

type localUploader struct {
	prefix string
}

func newLocalUploader(prefix string) (*localUploader, error) {
	return &localUploader{prefix: prefix}, nil
}

func (u *localUploader) upload(localFilepath, storageFilepath string, _ types.OutputType) (string, int64, error) {
	storageFilepath = path.Join(u.prefix, storageFilepath)

	stat, err := os.Stat(localFilepath)
	if err != nil {
		return "", 0, err
	}

	dir, _ := path.Split(storageFilepath)
	if err = os.MkdirAll(dir, 0755); err != nil {
		return "", 0, err
	}

	tmp, err := os.Open(localFilepath)
	if err != nil {
		return "", 0, err
	}

	f, err := os.Create(storageFilepath)
	if err != nil {
		_ = tmp.Close()
		return "", 0, err
	}

	_, err = io.Copy(f, tmp)
	_ = f.Close()
	_ = tmp.Close()
	if err != nil {
		return "", 0, err
	}

	return storageFilepath, stat.Size(), nil
}
