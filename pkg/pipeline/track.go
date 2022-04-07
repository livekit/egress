package pipeline

import (
	"fmt"
	"strings"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/h264writer"
	"github.com/pion/webrtc/v3/pkg/media/ivfwriter"
	"github.com/pion/webrtc/v3/pkg/media/oggwriter"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/livekit-egress/pkg/pipeline/source"
)

type trackPipeline struct {
	source *source.SDKSource

	info   *livekit.EgressInfo
	closed chan struct{}
}

func NewTrackPipeline(p *params.Params) (*trackPipeline, error) {
	pipeline := &trackPipeline{
		info:   p.Info,
		closed: make(chan struct{}),
	}

	s, err := source.NewSDKSource(p, pipeline.createMediaWriter)
	if err != nil {
		return nil, err
	}
	pipeline.source = s

	return pipeline, nil
}

func (p *trackPipeline) createMediaWriter(track *webrtc.TrackRemote) (media.Writer, error) {
	filename := fmt.Sprintf("%s-%v", track.ID(), time.Now().String())

	switch {
	case strings.EqualFold(track.Codec().MimeType, "video/vp8"):
		return ivfwriter.New(filename + ".ivf")
	case strings.EqualFold(track.Codec().MimeType, "video/h264"):
		return h264writer.New(filename + ".h264")
	case strings.EqualFold(track.Codec().MimeType, "audio/opus"):
		return oggwriter.New(filename+".ogg", 48000, track.Codec().Channels)
	default:
		return nil, errors.ErrNotSupported(track.Codec().MimeType)
	}
}

func (p *trackPipeline) Info() *livekit.EgressInfo {
	return p.info
}

func (p *trackPipeline) Run() *livekit.EgressInfo {
	select {
	case <-p.source.EndRecording():
	case <-p.closed:
	}

	p.source.Close()
	return p.info
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
