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
	"path"
	"time"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/stats"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
	"github.com/livekit/storage"
)

const presignedExpiration = time.Hour * 24 * 7 // 7 days

type Uploader struct {
	primary       *store
	backup        *store
	primaryFailed bool
	info          *livekit.EgressInfo
	monitor       *stats.HandlerMonitor
}

type store struct {
	storage.Storage
	conf *config.StorageConfig
	name string
}

func New(conf, backup *config.StorageConfig, monitor *stats.HandlerMonitor, info *livekit.EgressInfo) (*Uploader, error) {
	p, err := getUploader(conf)
	if err != nil {
		return nil, err
	}

	u := &Uploader{
		primary: p,
		monitor: monitor,
		info:    info,
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

func getUploader(conf *config.StorageConfig) (*store, error) {
	if conf == nil {
		conf = &config.StorageConfig{}
	}

	var (
		s    storage.Storage
		err  error
		name string
	)
	switch {
	case conf.S3 != nil:
		s, err = storage.NewS3(conf.S3)
		name = "S3"
	case conf.GCP != nil:
		s, err = storage.NewGCP(conf.GCP)
		name = "GCP"
	case conf.Azure != nil:
		s, err = storage.NewAzure(conf.Azure)
		name = "Azure"
	case conf.AliOSS != nil:
		s, err = storage.NewAliOSS(conf.AliOSS)
		name = "AliOSS"
	default:
		s, err = storage.NewLocal(&storage.LocalConfig{})
		name = "Local"
	}
	if err != nil {
		return nil, err
	}

	return &store{
		Storage: s,
		conf:    conf,
		name:    name,
	}, nil
}

func uploadToProvider(s *store, localFilepath string, storageFilepath string, outputType types.OutputType) (location string, size int64, err error) {
	storageFilepath = path.Join(s.conf.Prefix, storageFilepath)

	location, size, err = s.UploadFile(localFilepath, storageFilepath, string(outputType))
	if err != nil {
		return "", 0, errors.ErrUploadFailed(s.name, err)
	}

	if s.conf.GeneratePresignedUrl {
		location, err = s.GeneratePresignedUrl(storageFilepath, presignedExpiration)
		if err != nil {
			return "", 0, errors.ErrUploadFailed(s.name, err)
		}
	}

	return location, size, nil
}

func (u *Uploader) Upload(
	localFilepath, storageFilepath string,
	outputType types.OutputType,
	deleteAfterUpload bool,
) (string, int64, error) {

	var primaryErr error
	if !u.primaryFailed {
		start := time.Now()
		location, size, err := uploadToProvider(u.primary, localFilepath, storageFilepath, outputType)
		elapsed := time.Since(start)

		if err == nil {
			if u.monitor != nil {
				u.monitor.IncUploadCountSuccess(string(outputType), float64(elapsed.Milliseconds()))
			}
			if deleteAfterUpload {
				_ = os.Remove(localFilepath)
			}
			return location, size, nil
		}
		if u.monitor != nil {
			u.monitor.IncUploadCountFailure(string(outputType), float64(elapsed.Milliseconds()))
		}
		u.primaryFailed = true
		primaryErr = err

	}

	if u.backup != nil {
		location, size, backupErr := uploadToProvider(u.backup, localFilepath, storageFilepath, outputType)
		if backupErr == nil {
			if u.info != nil {
				u.info.SetBackupUsed()
			}
			if u.monitor != nil {
				u.monitor.IncBackupStorageWrites(string(outputType))
			}
			if deleteAfterUpload {
				_ = os.Remove(localFilepath)
			}
			return location, size, nil
		}

		if primaryErr != nil {
			return "", 0, psrpc.NewErrorf(psrpc.InvalidArgument,
				"primary: %s\nbackup: %s", primaryErr.Error(), backupErr.Error())
		}
		return "", 0, psrpc.NewError(psrpc.InvalidArgument, backupErr)
	}

	return "", 0, primaryErr
}
