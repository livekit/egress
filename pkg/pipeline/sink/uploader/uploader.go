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
	"os"
	"time"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
)

const (
	maxRetries = 5
	minDelay   = time.Millisecond * 100
	maxDelay   = time.Second * 5
)

type uploader interface {
	upload(string, string, types.OutputType) (string, int64, error)
}

type Uploader struct {
	primary uploader
	backup  uploader
	monitor *stats.HandlerMonitor
}

func New(conf, backup *config.StorageConfig, monitor *stats.HandlerMonitor) (*Uploader, error) {
	p, err := getUploader(conf)
	if err != nil {
		return nil, err
	}

	u := &Uploader{
		primary: p,
		monitor: monitor,
	}

	if backup != nil {
		b, err := getUploader(backup)
		if err != nil {
			logger.Errorw("failed to create backup uploader", err)
		} else {
			u.backup = b
		}
	}

	return u, nil
}

func getUploader(conf *config.StorageConfig) (uploader, error) {
	switch {
	case conf == nil:
		return newLocalUploader("")
	case conf.S3 != nil:
		return newS3Uploader(conf.S3, conf.PathPrefix)
	case conf.GCP != nil:
		return newGCPUploader(conf.GCP, conf.PathPrefix)
	case conf.Azure != nil:
		return newAzureUploader(conf.Azure, conf.PathPrefix)
	case conf.AliOSS != nil:
		return newAliOSSUploader(conf.AliOSS, conf.PathPrefix)
	default:
		return newLocalUploader(conf.PathPrefix)
	}
}

func (u *Uploader) Upload(localFilepath, storageFilepath string, outputType types.OutputType, deleteAfterUpload bool) (string, int64, error) {
	start := time.Now()
	location, size, primaryErr := u.primary.upload(localFilepath, storageFilepath, outputType)
	elapsed := time.Since(start)

	if primaryErr == nil {
		// success
		u.monitor.IncUploadCountSuccess(string(outputType), float64(elapsed.Milliseconds()))
		if deleteAfterUpload {
			_ = os.Remove(localFilepath)
		}
		return location, size, nil
	}

	u.monitor.IncUploadCountFailure(string(outputType), float64(elapsed.Milliseconds()))
	if u.backup != nil {
		location, size, backupErr := u.backup.upload(localFilepath, storageFilepath, outputType)
		if backupErr == nil {
			u.monitor.IncBackupStorageWrites(string(outputType))
			return location, size, nil
		}

		return "", 0, psrpc.NewErrorf(psrpc.InvalidArgument,
			"primary and backup uploads failed: %s\n%s", primaryErr.Error(), backupErr.Error())
	}

	return "", 0, primaryErr
}
