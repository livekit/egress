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

	"github.com/aliyun/aliyun-oss-go-sdk/oss"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

type AliOSSUploader struct {
	conf *livekit.AliOSSUpload
}

func newAliOSSUploader(conf *livekit.AliOSSUpload) (uploader, error) {
	return &AliOSSUploader{
		conf: conf,
	}, nil
}

func (u *AliOSSUploader) upload(localFilePath, requestedPath string, _ types.OutputType) (string, int64, error) {
	stat, err := os.Stat(localFilePath)
	if err != nil {
		return "", 0, wrap("AliOSS", err)
	}

	client, err := oss.New(u.conf.Endpoint, u.conf.AccessKey, u.conf.Secret)
	if err != nil {
		return "", 0, wrap("AliOSS", err)
	}

	bucket, err := client.Bucket(u.conf.Bucket)
	if err != nil {
		return "", 0, wrap("AliOSS", err)
	}

	err = bucket.PutObjectFromFile(requestedPath, localFilePath)
	if err != nil {
		return "", 0, wrap("AliOSS", err)
	}

	return fmt.Sprintf("https://%s.%s/%s", u.conf.Bucket, u.conf.Endpoint, requestedPath), stat.Size(), nil
}
