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

	"github.com/aws/aws-sdk-go/aws"

	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/livekit"
)

type UploadConfig interface{}

type EgressS3Upload struct {
	*livekit.S3Upload
	MaxRetries    int
	MaxRetryDelay time.Duration
	MinRetryDelay time.Duration
	AwsLogLevel   aws.LogLevelType
}

func (p *PipelineConfig) getUploadConfig(req egress.UploadRequest) UploadConfig {
	if s3 := req.GetS3(); s3 != nil {
		s3Conf := &EgressS3Upload{
			S3Upload:      s3,
			MaxRetries:    3,
			MaxRetryDelay: time.Second * 5,
			MinRetryDelay: time.Millisecond * 100,
		}
		// merge in options from config (retry limit, delay and aws logging) if specified
		if p.S3 != nil {
			// parse config.yaml options and get defaults
			if s3Base, ok := p.ToUploadConfig().(*EgressS3Upload); ok {
				// merge into pipeline config created from request options
				s3Conf.MaxRetries = s3Base.MaxRetries
				s3Conf.MaxRetryDelay = s3Base.MaxRetryDelay
				s3Conf.MinRetryDelay = s3Base.MinRetryDelay
				s3Conf.AwsLogLevel = s3Base.AwsLogLevel
			}
		}
		return s3Conf
	}
	if gcp := req.GetGcp(); gcp != nil {
		return gcp
	}
	if azure := req.GetAzure(); azure != nil {
		return azure
	}
	if ali := req.GetAliOSS(); ali != nil {
		return ali
	}

	return p.ToUploadConfig()
}

func (c StorageConfig) ToUploadConfig() UploadConfig {
	if c.S3 != nil {
		s3 := &EgressS3Upload{
			S3Upload: &livekit.S3Upload{
				AccessKey:      c.S3.AccessKey,
				Secret:         c.S3.Secret,
				Region:         c.S3.Region,
				Endpoint:       c.S3.Endpoint,
				Bucket:         c.S3.Bucket,
				ForcePathStyle: c.S3.ForcePathStyle,
			},
			MaxRetries:    3,
			MaxRetryDelay: time.Second * 5,
			MinRetryDelay: time.Millisecond * 100,
		}
		if c.S3.ProxyConfig != nil {
			s3.Proxy = &livekit.ProxyConfig{
				Url:      c.S3.ProxyConfig.Url,
				Username: c.S3.ProxyConfig.Username,
				Password: c.S3.ProxyConfig.Password,
			}
		} else if c.S3.Proxy != "" {
			s3.Proxy = &livekit.ProxyConfig{
				Url: c.S3.Proxy,
			}
		}
		if c.S3.MaxRetries > 0 {
			s3.MaxRetries = c.S3.MaxRetries
		}
		if c.S3.MaxRetryDelay > 0 {
			s3.MaxRetryDelay = c.S3.MaxRetryDelay
		}
		if c.S3.MinRetryDelay > 0 {
			s3.MinRetryDelay = c.S3.MinRetryDelay
		}

		// Handle AWS log level
		switch c.S3.AwsLogLevel {
		case "LogDebugWithRequestRetries":
			s3.AwsLogLevel = aws.LogDebugWithRequestRetries
		case "LogDebug":
			s3.AwsLogLevel = aws.LogDebug
		case "LogDebugWithRequestErrors":
			s3.AwsLogLevel = aws.LogDebugWithRequestErrors
		case "LogDebugWithHTTPBody":
			s3.AwsLogLevel = aws.LogDebugWithHTTPBody
		case "LogDebugWithSigning":
			s3.AwsLogLevel = aws.LogDebugWithSigning
		default:
			s3.AwsLogLevel = aws.LogOff
		}

		return s3
	}
	if c.Azure != nil {
		return &livekit.AzureBlobUpload{
			AccountName:   c.Azure.AccountName,
			AccountKey:    c.Azure.AccountKey,
			ContainerName: c.Azure.ContainerName,
		}
	}
	if c.GCP != nil {
		gcp := &livekit.GCPUpload{
			Credentials: c.GCP.CredentialsJSON,
			Bucket:      c.GCP.Bucket,
		}
		if c.GCP.ProxyConfig != nil {
			gcp.Proxy = &livekit.ProxyConfig{
				Url:      c.GCP.ProxyConfig.Url,
				Username: c.GCP.ProxyConfig.Username,
				Password: c.GCP.ProxyConfig.Password,
			}
		}
		return gcp
	}
	if c.AliOSS != nil {
		return &livekit.AliOSSUpload{
			AccessKey: c.AliOSS.AccessKey,
			Secret:    c.AliOSS.Secret,
			Region:    c.AliOSS.Region,
			Endpoint:  c.AliOSS.Endpoint,
			Bucket:    c.AliOSS.Bucket,
		}
	}
	return nil
}
