package output

import (
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

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
		protocol: p.StreamProtocol,
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

func buildStreamSink(protocol livekit.StreamProtocol, url string) (*streamSink, error) {
	id := utils.NewGuid("")

	queue, err := gst.NewElementWithName("queue", fmt.Sprintf("queue_%s", id))
	if err != nil {
		return nil, err
	}
	queue.SetArg("leaky", "downstream")

	var sink *gst.Element
	switch protocol {
	case livekit.StreamProtocol_RTMP:
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
		// case livekit.StreamProtocol_SRT:
		// 	return nil, errors.ErrNotSupported("srt output")
	}

	return &streamSink{
		queue: queue,
		sink:  sink,
	}, nil
}
