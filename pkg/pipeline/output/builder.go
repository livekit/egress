package output

import (
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/protocol/utils"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/egress/pkg/pipeline/sink"
)

func buildFileOutputBin(p *params.Params) (*Bin, error) {
	// create elements
	sink, err := gst.NewElement("filesink")
	if err != nil {
		return nil, err
	}
	if err = sink.SetProperty("location", p.FileInfo.Filename); err != nil {
		return nil, err
	}
	if err = sink.SetProperty("sync", false); err != nil {
		return nil, err
	}

	// create bin
	bin := gst.NewBin("output")
	if err = bin.Add(sink); err != nil {
		return nil, err
	}

	// add ghost pad
	ghostPad := gst.NewGhostPad("sink", sink.GetStaticPad("sink"))
	if !bin.AddPad(ghostPad.Pad) {
		return nil, errors.ErrGhostPadFailed
	}

	return &Bin{
		bin:    bin,
		logger: p.Logger,
	}, nil
}

func buildStreamOutputBin(p *params.Params) (*Bin, error) {
	// create elements
	tee, err := gst.NewElement("tee")
	if err != nil {
		return nil, err
	}

	bin := gst.NewBin("output")
	if err = bin.Add(tee); err != nil {
		return nil, err
	}

	b := &Bin{
		bin:      bin,
		protocol: p.OutputType,
		tee:      tee,
		sinks:    make(map[string]*streamSink),
		logger:   p.Logger,
	}

	for _, url := range p.StreamUrls {
		sink, err := buildStreamSink(b.protocol, url)
		if err != nil {
			return nil, err
		}

		if err = bin.AddMany(sink.queue, sink.sink); err != nil {
			return nil, err
		}

		b.sinks[url] = sink
	}

	// add ghost pad
	ghostPad := gst.NewGhostPad("sink", tee.GetStaticPad("sink"))
	if !bin.AddPad(ghostPad.Pad) {
		return nil, errors.ErrGhostPadFailed
	}

	return b, nil
}

func buildSegmentedOutputBin(p *params.Params) (*Bin, error) {
	// Create Sink
	hlsSink, err := sink.NewHlsSink(p)
	if err != nil {
		return nil, err
	}

	// create elements
	sink := hlsSink.GetAppSink().Element

	// create bin
	bin := gst.NewBin("output")
	if err = bin.Add(sink); err != nil {
		return nil, err
	}

	// add ghost pad
	ghostPad := gst.NewGhostPad("sink", sink.GetStaticPad("sink"))
	if !bin.AddPad(ghostPad.Pad) {
		return nil, errors.ErrGhostPadFailed
	}

	return &Bin{
		bin:    bin,
		logger: p.Logger,
	}, nil
}

func buildStreamSink(protocol params.OutputType, url string) (*streamSink, error) {
	id := utils.NewGuid("")

	queue, err := gst.NewElementWithName("queue", fmt.Sprintf("queue_%s", id))
	if err != nil {
		return nil, err
	}
	queue.SetArg("leaky", "downstream")

	var sink *gst.Element
	switch protocol {
	case params.OutputTypeRTMP:
		sink, err = gst.NewElementWithName("rtmp2sink", fmt.Sprintf("sink_%s", id))
		if err != nil {
			return nil, err
		}
		if err = sink.SetProperty("sync", false); err != nil {
			return nil, err
		}
		if err = sink.Set("location", url); err != nil {
			return nil, err
		}
	}

	return &streamSink{
		queue: queue,
		sink:  sink,
	}, nil
}

func buildWebsocketOutputBin(p *params.Params) (*Bin, error) {
	writer, err := newWebSocketSink(p.WebsocketUrl, params.MimeTypeRaw, p.Logger, p.MutedChan)
	if err != nil {
		return nil, err
	}

	sink, err := app.NewAppSink()
	if err != nil {
		return nil, err
	}

	sink.SetCallbacks(&app.SinkCallbacks{
		EOSFunc: func(appSink *app.Sink) {
			// Close writer on EOS
			if err = writer.Close(); err != nil {
				p.Logger.Errorw("cannot close WS sink", err)
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
			_, err = writer.Write(samples)
			if err != nil {
				p.Logger.Errorw("cannot read AppSink samples", err)
				return gst.FlowError
			}
			return gst.FlowOK
		},
	})

	bin := gst.NewBin("output")
	if err = bin.Add(sink.Element); err != nil {
		return nil, err
	}

	ghostPad := gst.NewGhostPad("sink", sink.GetStaticPad("sink"))
	if !bin.AddPad(ghostPad.Pad) {
		return nil, errors.ErrGhostPadFailed
	}

	return &Bin{
		bin:    bin,
		logger: p.Logger,
	}, nil
}
