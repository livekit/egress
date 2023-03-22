package sink

import (
	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/types"
)

type Sink interface {
	Start() error
	Finalize() error
	Cleanup()
}

func CreateSinks(p *config.PipelineConfig) (map[types.EgressType]Sink, error) {
	sinks := make(map[types.EgressType]Sink)
	for egressType, out := range p.Outputs {
		u, err := uploader.New(out.UploadConfig)
		if err != nil {
			return nil, err
		}

		switch egressType {
		case types.EgressTypeFile:
			sinks[egressType] = newFileSink(u, p, out)

		case types.EgressTypeSegments:
			s, err := newSegmentSink(u, p, out)
			if err != nil {
				return nil, err
			}
			sinks[egressType] = s

		case types.EgressTypeStream:
			// no sink needed

		case types.EgressTypeWebsocket:
			s, err := newWebsocketSink(out.WebsocketUrl, types.MimeTypeRawAudio)
			if err != nil {
				return nil, err
			}
			sinks[egressType] = s
		}
	}

	return sinks, nil
}
