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
	for egressType, c := range p.Outputs {
		switch egressType {
		case types.EgressTypeFile:
			o := c.(*config.FileConfig)

			u, err := uploader.New(o.UploadConfig)
			if err != nil {
				return nil, err
			}

			sinks[egressType] = newFileSink(u, p, o)

		case types.EgressTypeSegments:
			o := c.(*config.SegmentConfig)

			u, err := uploader.New(o.UploadConfig)
			if err != nil {
				return nil, err
			}

			s, err := newSegmentSink(u, p, o)
			if err != nil {
				return nil, err
			}
			sinks[egressType] = s

		case types.EgressTypeStream:
			// no sink needed

		case types.EgressTypeWebsocket:
			o := c.(*config.StreamConfig)

			s, err := newWebsocketSink(o, types.MimeTypeRawAudio)
			if err != nil {
				return nil, err
			}
			sinks[egressType] = s
		}
	}

	return sinks, nil
}
