package config

import (
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/egress/pkg/util"
	"github.com/livekit/protocol/livekit"
)

type OutputConfig interface {
	GetOutputType() types.OutputType
}

type outputConfig struct {
	types.OutputType
}

func (o outputConfig) GetOutputType() types.OutputType {
	return o.OutputType
}

type EncodedOutput interface {
	GetFile() *livekit.EncodedFileOutput
	GetStream() *livekit.StreamOutput
	GetSegments() *livekit.SegmentedFileOutput
	GetFileOutputs() []*livekit.EncodedFileOutput
	GetStreamOutputs() []*livekit.StreamOutput
	GetSegmentOutputs() []*livekit.SegmentedFileOutput
}

func (p *PipelineConfig) updateEncodedOutputs(req EncodedOutput) error {
	files := req.GetFileOutputs()
	streams := req.GetStreamOutputs()
	segments := req.GetSegmentOutputs()

	// file output
	var file *livekit.EncodedFileOutput
	switch len(files) {
	case 0:
		file = req.GetFile()
	case 1:
		file = files[0]
	default:
		return errors.ErrInvalidInput("multiple file outputs")
	}
	if file != nil {
		conf, err := p.getEncodedFileConfig(file)
		if err != nil {
			return err
		}

		p.Outputs[types.EgressTypeFile] = conf
		p.OutputCount++

		p.Info.FileResults = []*livekit.FileInfo{conf.FileInfo}
		if len(streams)+len(segments) == 0 {
			p.Info.Result = &livekit.EgressInfo_File{File: conf.FileInfo}
			return nil
		}
	}

	// stream output
	var stream *livekit.StreamOutput
	switch len(streams) {
	case 0:
		stream = req.GetStream()
	case 1:
		stream = streams[0]
	default:
		return errors.ErrInvalidInput("multiple stream outputs")
	}
	if stream != nil {
		conf, err := p.getStreamConfig(types.OutputTypeRTMP, stream.Urls)
		if err != nil {
			return err
		}

		p.Outputs[types.EgressTypeStream] = conf
		p.OutputCount += len(stream.Urls)

		streamInfoList := make([]*livekit.StreamInfo, 0, len(conf.StreamInfo))
		for _, info := range conf.StreamInfo {
			streamInfoList = append(streamInfoList, info)
		}
		p.Info.StreamResults = streamInfoList
		if len(files)+len(segments) == 0 {
			// empty stream output only valid in combination with other outputs
			if len(stream.Urls) == 0 {
				return errors.ErrInvalidInput("stream url")
			}

			p.Info.Result = &livekit.EgressInfo_Stream{Stream: &livekit.StreamInfoList{Info: streamInfoList}}
			return nil
		}
	}

	// segment output
	var segment *livekit.SegmentedFileOutput
	switch len(segments) {
	case 0:
		segment = req.GetSegments()
	case 1:
		segment = segments[0]
	default:
		return errors.ErrInvalidInput("multiple segmented file outputs")
	}
	if segment != nil {
		conf, err := p.getSegmentConfig(segment)
		if err != nil {
			return err
		}

		p.Outputs[types.EgressTypeSegments] = conf
		p.OutputCount++

		p.Info.SegmentResults = []*livekit.SegmentsInfo{conf.SegmentsInfo}
		if len(streams)+len(segments) == 0 {
			p.Info.Result = &livekit.EgressInfo_Segments{Segments: conf.SegmentsInfo}
			return nil
		}
	}

	if p.OutputCount == 0 {
		return errors.ErrInvalidInput("output")
	}

	return nil
}

func (p *PipelineConfig) updateDirectOutput(req *livekit.TrackEgressRequest) error {
	switch o := req.Output.(type) {
	case *livekit.TrackEgressRequest_File:
		conf, err := p.getDirectFileConfig(o.File)
		if err != nil {
			return err
		}

		p.Info.FileResults = []*livekit.FileInfo{conf.FileInfo}
		p.Info.Result = &livekit.EgressInfo_File{File: conf.FileInfo}

		p.Outputs[types.EgressTypeFile] = conf
		p.OutputCount = 1

	case *livekit.TrackEgressRequest_WebsocketUrl:
		conf, err := p.getStreamConfig(types.OutputTypeRaw, []string{o.WebsocketUrl})
		if err != nil {
			return err
		}

		streamInfoList := make([]*livekit.StreamInfo, 0, len(conf.StreamInfo))
		for _, info := range conf.StreamInfo {
			streamInfoList = append(streamInfoList, info)
		}
		p.Info.StreamResults = streamInfoList
		p.Info.Result = &livekit.EgressInfo_Stream{Stream: &livekit.StreamInfoList{Info: streamInfoList}}

		p.Outputs[types.EgressTypeWebsocket] = conf
		p.OutputCount = 1

	default:
		return errors.ErrInvalidInput("output")
	}

	return nil
}

type uploader interface {
	GetS3() *livekit.S3Upload
	GetGcp() *livekit.GCPUpload
	GetAzure() *livekit.AzureBlobUpload
	GetAliOSS() *livekit.AliOSSUpload
}

func (p *PipelineConfig) getUploadConfig(upload uploader) interface{} {
	if s3 := upload.GetS3(); s3 != nil {
		return s3
	}
	if gcp := upload.GetGcp(); gcp != nil {
		return gcp
	}
	if azure := upload.GetAzure(); azure != nil {
		return azure
	}
	if ali := upload.GetAliOSS(); ali != nil {
		return ali
	}
	if p.S3 != nil {
		return p.S3.ToS3Upload()
	}
	if p.GCP != nil {
		return p.GCP.ToGCPUpload()
	}
	if p.Azure != nil {
		return p.Azure.ToAzureUpload()
	}
	if p.AliOSS != nil {
		return p.AliOSS.ToAliOSSUpload()
	}
	return nil
}

func redactEncodedOutputs(out EncodedOutput) {
	if file := out.GetFile(); file != nil {
		redactUpload(file)
	} else if stream := out.GetStream(); stream != nil {
		redactStreamKeys(stream)
	} else if segment := out.GetSegments(); segment != nil {
		redactUpload(segment)
	} else {
		if files := out.GetFileOutputs(); len(files) == 1 {
			redactUpload(files[0])
		}
		if streams := out.GetStreamOutputs(); len(streams) == 1 {
			redactStreamKeys(streams[0])
		}
		if segments := out.GetSegmentOutputs(); len(segments) == 1 {
			redactUpload(segments[0])
		}
	}
}

func redactUpload(upload uploader) {
	if s3 := upload.GetS3(); s3 != nil {
		s3.AccessKey = util.Redact(s3.AccessKey)
		s3.Secret = util.Redact(s3.Secret)
		return
	}

	if gcp := upload.GetGcp(); gcp != nil {
		gcp.Credentials = util.Redact(gcp.Credentials)
		return
	}

	if azure := upload.GetAzure(); azure != nil {
		azure.AccountName = util.Redact(azure.AccountName)
		azure.AccountKey = util.Redact(azure.AccountKey)
		return
	}

	if aliOSS := upload.GetAliOSS(); aliOSS != nil {
		aliOSS.AccessKey = util.Redact(aliOSS.AccessKey)
		aliOSS.Secret = util.Redact(aliOSS.Secret)
		return
	}
}

func redactStreamKeys(stream *livekit.StreamOutput) {
	for i, url := range stream.Urls {
		if redacted, ok := util.RedactStreamKey(url); ok {
			stream.Urls[i] = redacted
		}
	}
}
