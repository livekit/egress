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

//go:build integration

package test

import (
	"encoding/json"
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/storage"
)

func loadManifest(t *testing.T, c *config.StorageConfig, localFilepath, storageFilepath string) *config.Manifest {
	download(t, c, localFilepath, storageFilepath, false)
	defer os.Remove(localFilepath)

	b, err := os.ReadFile(localFilepath)
	require.NoError(t, err)

	m := &config.Manifest{}
	err = json.Unmarshal(b, m)
	require.NoError(t, err)

	return m
}

// getStorage returns a storage client for cloud-backed configs, or (nil, "")
// when the config is nil or local — callers should skip in that case.
func getStorage(t *testing.T, c *config.StorageConfig) (storage.Storage, string) {
	if c == nil {
		return nil, ""
	}

	var (
		conf     storage.Config
		provider string
	)
	switch {
	case c.S3 != nil:
		conf, provider = c.S3, "s3"
	case c.GCP != nil:
		conf, provider = c.GCP, "gcp"
	case c.Azure != nil:
		conf, provider = c.Azure, "azure"
	case c.AliOSS != nil:
		conf, provider = c.AliOSS, "alioss"
	default:
		return nil, ""
	}

	s, err := storage.New(conf)
	require.NoError(t, err)
	return s, provider
}

// requireNotUploaded fails the test if storageFilepath exists in storage.
// Local/nil storage is skipped — there's nothing to probe.
func requireNotUploaded(t *testing.T, c *config.StorageConfig, storageFilepath string) {
	s, _ := getStorage(t, c)
	if s == nil {
		return
	}

	localPath := path.Join(t.TempDir(), path.Base(storageFilepath))
	_, err := s.DownloadFile(localPath, storageFilepath)
	require.Errorf(t, err, "file %s should not have been uploaded", storageFilepath)
}

func download(t *testing.T, c *config.StorageConfig, localFilepath, storageFilepath string, delete bool) {
	s, provider := getStorage(t, c)
	if s == nil {
		return
	}

	logger.Debugw(provider+" download", "localFilepath", localFilepath, "storageFilepath", storageFilepath)

	_, err := s.DownloadFile(localFilepath, storageFilepath)
	require.NoError(t, err)

	if delete {
		require.NoError(t, s.DeleteObject(storageFilepath))
	}
}
