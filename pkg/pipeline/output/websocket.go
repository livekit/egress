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

package output

import (
	"io"

	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/sink"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

type WebsocketOutput struct {
	*outputBase

	conf *config.PipelineConfig
	sink *app.Sink
}

func (b *Bin) buildWebsocketOutput(p *config.PipelineConfig) (*WebsocketOutput, error) {
	base, err := b.buildOutputBase(p, types.EgressTypeWebsocket)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	appSink, err := app.NewAppSink()
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	if err = b.bin.Add(appSink.Element); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return &WebsocketOutput{
		outputBase: base,
		conf:       p,
		sink:       appSink,
	}, nil
}

func (o *WebsocketOutput) SetSink(writer *sink.WebsocketSink, eosFunc func(*app.Sink)) {
	o.sink.SetCallbacks(&app.SinkCallbacks{
		EOSFunc: eosFunc,
		NewSampleFunc: func(appSink *app.Sink) gst.FlowReturn {
			// Pull the sample that triggered this callback
			sample := appSink.PullSample()
			if sample == nil {
				logger.Debugw("unexpected flow return",
					"flow", "EOS",
					"reason", "nil sample",
				)
				return gst.FlowEOS
			}

			// Retrieve the buffer from the sample
			buffer := sample.GetBuffer()
			if buffer == nil {
				logger.Debugw("unexpected flow return",
					"flow", "Error",
					"reason", "nil buffer",
				)
				return gst.FlowError
			}

			// Map the buffer to READ operation
			samples := buffer.Map(gst.MapRead).Bytes()

			// From the extracted bytes, send to writer
			_, err := writer.Write(samples)
			if err != nil {
				if err == io.EOF {
					logger.Debugw("unexpected flow return",
						"flow", "EOS",
						"reason", "Write returned EOF",
					)
					return gst.FlowEOS
				}
				o.conf.OnFailure(err)
				logger.Debugw("unexpected flow return",
					"flow", "Error",
					"reason", err.Error(),
				)
				return gst.FlowError
			}

			return gst.FlowOK
		},
	})
}

func (o *WebsocketOutput) Link() error {
	// link audio to sink
	if o.audioQueue != nil {
		if err := o.audioQueue.Link(o.sink.Element); err != nil {
			return errors.ErrPadLinkFailed("audio queue", "app sink", err.Error())
		}
	}

	return nil
}
