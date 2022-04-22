package pipeline

import (
	"os"

	"github.com/livekit/livekit-egress/pkg/pipeline/sink"
	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/livekit-egress/pkg/pipeline/source"
)

type trackPipeline struct {
	*params.Params

	source *source.SDKSource
	closed chan struct{}
}

func NewTrackPipeline(p *params.Params) (*trackPipeline, error) {
	pipeline := &trackPipeline{
		Params: p,
		closed: make(chan struct{}),
	}

	s, err := source.NewSDKSource(p)
	if err != nil {
		return nil, err
	}
	pipeline.source = s

	return pipeline, nil
}

func (p *trackPipeline) GetInfo() *livekit.EgressInfo {
	return p.Info
}

func (p *trackPipeline) Run() *livekit.EgressInfo {
	select {
	case <-p.source.EndRecording():
	case <-p.closed:
	}

	p.source.Close()

	// update file size
	fileInfo, err := os.Stat(p.Filename)
	if err == nil {
		p.FileInfo.Size = fileInfo.Size()
	} else {
		p.Logger.Errorw("could not read file size", err)
	}

	var location string
	switch u := p.FileUpload.(type) {
	case *livekit.S3Upload:
		location = "S3"
		p.Logger.Debugw("uploading to s3")
		p.FileInfo.Location, err = sink.UploadS3(u, p.FileParams)
	case *livekit.GCPUpload:
		location = "GCP"
		p.Logger.Debugw("uploading to gcp")
		p.FileInfo.Location, err = sink.UploadGCP(u, p.FileParams)
	case *livekit.AzureBlobUpload:
		location = "Azure"
		p.Logger.Debugw("uploading to azure")
		p.FileInfo.Location, err = sink.UploadAzure(u, p.FileParams)
	default:
		p.FileInfo.Location = p.Filepath
	}
	if err != nil {
		p.Logger.Errorw("could not upload file", err, "location", location)
		p.Info.Error = errors.ErrUploadFailed(location, err)
	}

	p.FileInfo.StartedAt = p.source.GetStartTime()
	p.FileInfo.EndedAt = p.source.GetEndTime()

	return p.Info
}

func (p *trackPipeline) UpdateStream(_ *livekit.UpdateStreamRequest) error {
	return errors.ErrInvalidRPC
}

func (p *trackPipeline) Stop() {
	select {
	case <-p.closed:
		return
	default:
		close(p.closed)
	}
}
