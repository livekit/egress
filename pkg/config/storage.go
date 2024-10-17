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

	"github.com/livekit/protocol/egress"
)

type StorageConfig struct {
	PathPrefix           string `yaml:"prefix"` // prefix applied to all filenames
	GeneratePresignedUrl bool   `yaml:"generate_presigned_url"`

	S3     *S3Config    `yaml:"s3"`     // upload to s3
	Azure  *AzureConfig `yaml:"azure"`  // upload to azure
	GCP    *GCPConfig   `yaml:"gcp"`    // upload to gcp
	AliOSS *S3Config    `yaml:"alioss"` // upload to aliyun
}

type S3Config struct {
	AccessKey      string       `yaml:"access_key"`    // (env AWS_ACCESS_KEY_ID)
	Secret         string       `yaml:"secret"`        // (env AWS_SECRET_ACCESS_KEY)
	SessionToken   string       `yaml:"session_token"` // (env AWS_SESSION_TOKEN)
	Region         string       `yaml:"region"`        // (env AWS_DEFAULT_REGION)
	Endpoint       string       `yaml:"endpoint"`
	Bucket         string       `yaml:"bucket"`
	ForcePathStyle bool         `yaml:"force_path_style"`
	ProxyConfig    *ProxyConfig `yaml:"proxy_config"`

	MaxRetries    int           `yaml:"max_retries"`
	MaxRetryDelay time.Duration `yaml:"max_retry_delay"`
	MinRetryDelay time.Duration `yaml:"min_retry_delay"`

	Metadata           map[string]string `yaml:"metadata"`
	Tagging            string            `yaml:"tagging"`
	ContentDisposition string            `yaml:"content_disposition"`
}

type AzureConfig struct {
	AccountName   string `yaml:"account_name"` // (env AZURE_STORAGE_ACCOUNT)
	AccountKey    string `yaml:"account_key"`  // (env AZURE_STORAGE_KEY)
	ContainerName string `yaml:"container_name"`
}

type GCPConfig struct {
	CredentialsJSON string       `yaml:"credentials_json"` // (env GOOGLE_APPLICATION_CREDENTIALS)
	Bucket          string       `yaml:"bucket"`
	ProxyConfig     *ProxyConfig `yaml:"proxy_config"`
}

func (p *PipelineConfig) getStorageConfig(req egress.UploadRequest) *StorageConfig {
	sc := &StorageConfig{}
	if p.StorageConfig != nil {
		sc.PathPrefix = p.StorageConfig.PathPrefix
		sc.GeneratePresignedUrl = p.StorageConfig.GeneratePresignedUrl
	}

	if s3 := req.GetS3(); s3 != nil {
		sc.S3 = &S3Config{
			AccessKey:          s3.AccessKey,
			Secret:             s3.Secret,
			SessionToken:       s3.SessionToken,
			Region:             s3.Region,
			Endpoint:           s3.Endpoint,
			Bucket:             s3.Bucket,
			ForcePathStyle:     s3.ForcePathStyle,
			Metadata:           s3.Metadata,
			Tagging:            s3.Tagging,
			ContentDisposition: s3.ContentDisposition,
		}
		if p.StorageConfig != nil && p.StorageConfig.S3 != nil {
			sc.S3.MaxRetries = p.StorageConfig.S3.MaxRetries
			sc.S3.MaxRetryDelay = p.StorageConfig.S3.MaxRetryDelay
			sc.S3.MinRetryDelay = p.StorageConfig.S3.MinRetryDelay
		}
		if s3.Proxy != nil {
			sc.S3.ProxyConfig = &ProxyConfig{
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
		return sc
	}

	if gcp := req.GetGcp(); gcp != nil {
		sc.GCP = &GCPConfig{
			CredentialsJSON: gcp.Credentials,
			Bucket:          gcp.Bucket,
		}
		if gcp.Proxy != nil {
			sc.GCP.ProxyConfig = &ProxyConfig{
				Url:      gcp.Proxy.Url,
				Username: gcp.Proxy.Username,
				Password: gcp.Proxy.Password,
			}
		}
		return sc
	}

	if azure := req.GetAzure(); azure != nil {
		sc.Azure = &AzureConfig{
			AccountName:   azure.AccountName,
			AccountKey:    azure.AccountKey,
			ContainerName: azure.ContainerName,
		}
		return sc
	}

	if ali := req.GetAliOSS(); ali != nil {
		sc.AliOSS = &S3Config{
			AccessKey: ali.AccessKey,
			Secret:    ali.Secret,
			Region:    ali.Region,
			Endpoint:  ali.Endpoint,
			Bucket:    ali.Bucket,
		}
		return sc
	}

	return p.StorageConfig
}
