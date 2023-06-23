package config

import (
	"github.com/livekit/egress/pkg/util"
	"github.com/livekit/protocol/livekit"
)

type UploadConfig interface{}

type uploadRequest interface {
	GetS3() *livekit.S3Upload
	GetGcp() *livekit.GCPUpload
	GetAzure() *livekit.AzureBlobUpload
	GetAliOSS() *livekit.AliOSSUpload
}

func (p *PipelineConfig) getUploadConfig(req uploadRequest) UploadConfig {
	if s3 := req.GetS3(); s3 != nil {
		return s3
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
		return &livekit.S3Upload{
			AccessKey:      c.S3.AccessKey,
			Secret:         c.S3.Secret,
			Region:         c.S3.Region,
			Endpoint:       c.S3.Endpoint,
			Bucket:         c.S3.Bucket,
			ForcePathStyle: c.S3.ForcePathStyle,
		}
	}
	if c.Azure != nil {
		return &livekit.AzureBlobUpload{
			AccountName:   c.Azure.AccountName,
			AccountKey:    c.Azure.AccountKey,
			ContainerName: c.Azure.ContainerName,
		}
	}
	if c.GCP != nil {
		return &livekit.GCPUpload{
			Credentials: c.GCP.CredentialsJSON,
			Bucket:      c.GCP.Bucket,
		}
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

func redactUpload(req uploadRequest) {
	if s3 := req.GetS3(); s3 != nil {
		s3.AccessKey = util.Redact(s3.AccessKey, "{access_key}")
		s3.Secret = util.Redact(s3.Secret, "{secret}")
		return
	}

	if gcp := req.GetGcp(); gcp != nil {
		gcp.Credentials = util.Redact(gcp.Credentials, "{credentials}")
		return
	}

	if azure := req.GetAzure(); azure != nil {
		azure.AccountName = util.Redact(azure.AccountName, "{account_name}")
		azure.AccountKey = util.Redact(azure.AccountKey, "{account_key}")
		return
	}

	if aliOSS := req.GetAliOSS(); aliOSS != nil {
		aliOSS.AccessKey = util.Redact(aliOSS.AccessKey, "{access_key}")
		aliOSS.Secret = util.Redact(aliOSS.Secret, "{secret}")
		return
	}
}
