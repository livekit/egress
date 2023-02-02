package output

import (
	"io"

	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/pipeline/sink"
	"github.com/livekit/protocol/logger"
)

type WebsocketOutput struct {
	sink *app.Sink
}

func buildWebsocketOutput(bin *gst.Bin) (*WebsocketOutput, error) {
	appSink, err := app.NewAppSink()
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	if err = bin.Add(appSink.Element); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return &WebsocketOutput{
		sink: appSink,
	}, nil
}

func (o *WebsocketOutput) SetSink(writer *sink.WebsocketSink) {
	o.sink.SetCallbacks(&app.SinkCallbacks{
		EOSFunc: func(appSink *app.Sink) {
			// Close writer on EOS
			if err := writer.Close(); err != nil && !errors.Is(err, io.EOF) {
				logger.Errorw("cannot close WS sink", err)
			}
		},
		NewSampleFunc: func(appSink *app.Sink) gst.FlowReturn {
			// Pull the sample that triggered this callback
			sample := appSink.PullSample()
			if sample == nil {
				return gst.FlowEOS
			}

			// Retrieve the buffer from the sample
			buffer := sample.GetBuffer()
			if buffer == nil {
				return gst.FlowError
			}

			// Map the buffer to READ operation
			samples := buffer.Map(gst.MapRead).Bytes()

			// From the extracted bytes, send to writer
			_, err := writer.Write(samples)
			if err != nil && !errors.Is(err, io.EOF) {
				logger.Errorw("cannot read AppSink samples", err)
				return gst.FlowError
			}
			return gst.FlowOK
		},
	})
}

func (o *WebsocketOutput) Link(audioTee, _ *gst.Element) error {
	// link audio to sink
	if audioTee != nil {
		teePad := audioTee.GetRequestPad("src_%u")
		sinkPad := o.sink.GetStaticPad("sink")
		if err := builder.LinkPads("audio tee", teePad, "appsink", sinkPad); err != nil {
			return err
		}
	}

	return nil
}
