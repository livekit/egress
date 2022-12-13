package config

import (
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
)

func (p *PipelineConfig) cloneAndRedactRequest(request *livekit.StartEgressRequest) {
	switch req := request.Request.(type) {
	case *livekit.StartEgressRequest_RoomComposite:
		clone := proto.Clone(req.RoomComposite).(*livekit.RoomCompositeEgressRequest)
		p.Info.Request = &livekit.EgressInfo_RoomComposite{
			RoomComposite: clone,
		}

		if f := clone.GetFile(); f != nil {
			p.cloneAndRedact(f)
		} else if s := clone.GetSegments(); s != nil {
			p.cloneAndRedact(s)
		}

	case *livekit.StartEgressRequest_Web:
		clone := proto.Clone(req.Web).(*livekit.WebEgressRequest)
		p.Info.Request = &livekit.EgressInfo_Web{
			Web: clone,
		}
		if f := clone.GetFile(); f != nil {
			p.cloneAndRedact(f)
		} else if s := clone.GetSegments(); s != nil {
			p.cloneAndRedact(s)
		}

	case *livekit.StartEgressRequest_TrackComposite:
		clone := proto.Clone(req.TrackComposite).(*livekit.TrackCompositeEgressRequest)
		p.Info.Request = &livekit.EgressInfo_TrackComposite{
			TrackComposite: clone,
		}
		if f := clone.GetFile(); f != nil {
			p.cloneAndRedact(f)
		} else if s := clone.GetSegments(); s != nil {
			p.cloneAndRedact(s)
		}

	case *livekit.StartEgressRequest_Track:
		clone := proto.Clone(req.Track).(*livekit.TrackEgressRequest)
		p.Info.Request = &livekit.EgressInfo_Track{
			Track: clone,
		}
		if f := clone.GetFile(); f != nil {
			p.cloneAndRedact(f)
		}
	}

	if p.UploadConfig == nil {
		if p.S3 != nil {
			p.UploadConfig = p.S3.ToS3Upload()
		} else if p.Azure != nil {
			p.UploadConfig = p.Azure.ToAzureUpload()
		} else if p.GCP != nil {
			p.UploadConfig = p.GCP.ToGCPUpload()
		} else if p.AliOSS != nil {
			p.UploadConfig = p.AliOSS.ToAliOSSUpload()
		}
	}
}

type iUpload interface {
	GetS3() *livekit.S3Upload
	GetGcp() *livekit.GCPUpload
	GetAzure() *livekit.AzureBlobUpload
	GetAliOSS() *livekit.AliOSSUpload
}

func (p *PipelineConfig) cloneAndRedact(req iUpload) {
	if s3 := req.GetS3(); s3 != nil {
		p.UploadConfig = proto.Clone(s3)
		s3.AccessKey = redact(s3.AccessKey)
		s3.Secret = redact(s3.Secret)
		return
	}

	if azure := req.GetAzure(); azure != nil {
		p.UploadConfig = proto.Clone(azure)
		azure.AccountName = redact(azure.AccountName)
		azure.AccountKey = redact(azure.AccountKey)
	}

	if gcp := req.GetGcp(); gcp != nil {
		p.UploadConfig = proto.Clone(gcp)
		gcp.Credentials = []byte(redact(string(gcp.Credentials)))
	}

	if aliOSS := req.GetAliOSS(); aliOSS != nil {
		p.UploadConfig = proto.Clone(aliOSS)
		aliOSS.AccessKey = redact(aliOSS.AccessKey)
		aliOSS.Secret = redact(aliOSS.Secret)
	}
}

func redact(s string) string {
	return strings.Repeat("*", len(s))
}
