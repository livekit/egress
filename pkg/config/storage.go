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

package config

import (
	"time"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/storage"
)

type StorageConfig struct {
	Prefix               string `yaml:"prefix"` // prefix applied to all filenames
	GeneratePresignedUrl bool   `yaml:"generate_presigned_url"`

	S3     *storage.S3Config     `yaml:"s3"`     // upload to s3
	Azure  *storage.AzureConfig  `yaml:"azure"`  // upload to azure
	GCP    *storage.GCPConfig    `yaml:"gcp"`    // upload to gcp
	AliOSS *storage.AliOSSConfig `yaml:"alioss"` // upload to aliyun
}

func (p *PipelineConfig) getStorageConfig(req egress.UploadRequest) (*StorageConfig, error) {
	sc := &StorageConfig{}
	if p.StorageConfig != nil {
		sc.Prefix = p.StorageConfig.Prefix
		sc.GeneratePresignedUrl = p.StorageConfig.GeneratePresignedUrl
	}

	if s3 := req.GetS3(); s3 != nil {
		sc.S3 = &storage.S3Config{
			AccessKey:            s3.AccessKey,
			Secret:               s3.Secret,
			SessionToken:         s3.SessionToken,
			AssumeRoleArn:        s3.AssumeRoleArn,
			AssumeRoleExternalId: s3.AssumeRoleExternalId,
			Region:               s3.Region,
			Endpoint:             s3.Endpoint,
			Bucket:               s3.Bucket,
			ForcePathStyle:       s3.ForcePathStyle,
			Metadata:             s3.Metadata,
			Tagging:              s3.Tagging,
			ContentDisposition:   s3.ContentDisposition,
		}
		if p.StorageConfig != nil && p.StorageConfig.S3 != nil {
			sc.S3.MaxRetries = p.StorageConfig.S3.MaxRetries
			sc.S3.MaxRetryDelay = p.StorageConfig.S3.MaxRetryDelay
			sc.S3.MinRetryDelay = p.StorageConfig.S3.MinRetryDelay
		}

		if sc.S3.AssumeRoleArn == "" {
			sc.S3.AssumeRoleArn = p.S3AssumeRoleArn
			sc.S3.AssumeRoleExternalId = p.S3AssumeRoleExternalID
		}

		if sc.S3.AssumeRoleArn != "" && sc.S3.AccessKey == "" {
			if p.S3AssumeRoleKey == "" {
				return nil, errors.ErrFeatureDisabled("S3 upload using AssumeRole")
			}
			// If an AssummedRole is set but not any AccessKey, default to using the one from conf. This is useful for uploading to S3
			// using an external account.
			sc.S3.AccessKey = p.S3AssumeRoleKey
			sc.S3.Secret = p.S3AssumeRoleSecret
		}

		if s3.Proxy != nil {
			sc.S3.ProxyConfig = &storage.ProxyConfig{
				Url:      s3.Proxy.Url,
				Username: s3.Proxy.Username,
				Password: s3.Proxy.Password,
			}
		}
		if sc.S3.MaxRetries == 0 {
			sc.S3.MaxRetries = 5
		}
		if sc.S3.MaxRetryDelay == 0 {
			sc.S3.MaxRetryDelay = time.Second * 5
		}
		if sc.S3.MinRetryDelay == 0 {
			sc.S3.MinRetryDelay = time.Millisecond * 100
		}
		return sc, nil
	}

	if gcp := req.GetGcp(); gcp != nil {
		sc.GCP = &storage.GCPConfig{
			CredentialsJSON: gcp.Credentials,
			Bucket:          gcp.Bucket,
		}
		if gcp.Proxy != nil {
			sc.GCP.ProxyConfig = &storage.ProxyConfig{
				Url:      gcp.Proxy.Url,
				Username: gcp.Proxy.Username,
				Password: gcp.Proxy.Password,
			}
		}
		return sc, nil
	}

	if azure := req.GetAzure(); azure != nil {
		sc.Azure = &storage.AzureConfig{
			AccountName:   azure.AccountName,
			AccountKey:    azure.AccountKey,
			ContainerName: azure.ContainerName,
		}
		return sc, nil
	}

	if ali := req.GetAliOSS(); ali != nil {
		sc.AliOSS = &storage.AliOSSConfig{
			AccessKey: ali.AccessKey,
			Secret:    ali.Secret,
			Endpoint:  ali.Endpoint,
			Bucket:    ali.Bucket,
		}
		return sc, nil
	}

	sc = p.StorageConfig
	if p.DisallowLocalStorage && (sc == nil || sc.IsLocal()) {
		return nil, errors.ErrInvalidInput("output")
	}

	return sc, nil
}

func (c *StorageConfig) IsLocal() bool {
	return c.S3 == nil && c.GCP == nil && c.Azure == nil && c.AliOSS == nil
}
