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

	"github.com/aliyun/aliyun-oss-go-sdk/oss"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
)

type AliOSSUploader struct {
	conf                 *config.S3Config
	prefix               string
	generatePresignedUrl bool
}

func newAliOSSUploader(c *config.StorageConfig) (uploader, error) {
	if c.GeneratePresignedUrl {
		return nil, errors.ErrUploadFailed("AliOSS", fmt.Errorf("presigned URLs not supported"))
	}

	conf := c.AliOSS
	return &AliOSSUploader{
		conf:                 conf,
		prefix:               c.Prefix,
		generatePresignedUrl: c.GeneratePresignedUrl,
	}, nil
}

func (u *AliOSSUploader) upload(localFilepath, storageFilepath string, _ types.OutputType) (string, int64, error) {
	storageFilepath = path.Join(u.prefix, storageFilepath)

	stat, err := os.Stat(localFilepath)
	if err != nil {
		return "", 0, errors.ErrUploadFailed("AliOSS", err)
	}

	client, err := oss.New(u.conf.Endpoint, u.conf.AccessKey, u.conf.Secret)
	if err != nil {
		return "", 0, errors.ErrUploadFailed("AliOSS", err)
	}

	bucket, err := client.Bucket(u.conf.Bucket)
	if err != nil {
		return "", 0, errors.ErrUploadFailed("AliOSS", err)
	}

	err = bucket.PutObjectFromFile(storageFilepath, localFilepath)
	if err != nil {
		return "", 0, errors.ErrUploadFailed("AliOSS", err)
	}

	return fmt.Sprintf("https://%s.%s/%s", u.conf.Bucket, u.conf.Endpoint, storageFilepath), stat.Size(), nil
}
