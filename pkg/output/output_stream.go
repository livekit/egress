package output

import (
	"fmt"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/livekit-egress/pkg/errors"
)

type streamOutputBin struct {
	protocol livekit.StreamProtocol
	bin      *gst.Bin
	tee      *gst.Element
	sinks    map[string]*streamSink
}

type streamSink struct {
	pad   string
	queue *gst.Element
	sink  *gst.Element
}

func newStreamOutputBin(protocol livekit.StreamProtocol, urls []string) (Bin, error) {
	// create elements
	tee, err := gst.NewElement("tee")
	if err != nil {
		return nil, err
	}

	bin := gst.NewBin("output")
	if err = bin.Add(tee); err != nil {
		return nil, err
	}

	b := &streamOutputBin{
		protocol: protocol,
		bin:      bin,
		tee:      tee,
		sinks:    make(map[string]*streamSink),
	}

	for _, url := range urls {
		sink, err := b.createStreamSink(url)
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

func (b *streamOutputBin) createStreamSink(url string) (*streamSink, error) {
	id := utils.NewGuid("")

	queue, err := gst.NewElementWithName("queue", fmt.Sprintf("queue_%s", id))
	if err != nil {
		return nil, err
	}
	queue.SetArg("leaky", "downstream")

	var sink *gst.Element
	switch b.protocol {
	case livekit.StreamProtocol_RTMP:
		sink, err = gst.NewElementWithName("rtmpsink", fmt.Sprintf("sink_%s", id))
		if err != nil {
			return nil, err
		}
		if err = sink.SetProperty("sync", false); err != nil {
			return nil, err
		}
		if err = sink.Set("location", url); err != nil {
			return nil, err
		}
	case livekit.StreamProtocol_SRT:
		return nil, errors.ErrNotSupported("srt output")
	}

	return &streamSink{
		queue: queue,
		sink:  sink,
	}, nil
}

func (b *streamOutputBin) LinkElements() error {
	for _, sink := range b.sinks {
		// link queue to rtmp sink
		if err := sink.queue.Link(sink.sink); err != nil {
			return err
		}

		pad := b.tee.GetRequestPad("src_%u")
		sink.pad = pad.GetName()

		// link tee to queue
		if linkReturn := pad.Link(sink.queue.GetStaticPad("sink")); linkReturn != gst.PadLinkOK {
			return fmt.Errorf("pad link: %s", linkReturn.String())
		}
	}

	return nil
}

func (b *streamOutputBin) Bin() *gst.Bin {
	return b.bin
}

func (b *streamOutputBin) AddSink(url string) error {
	if _, ok := b.sinks[url]; ok {
		return errors.ErrOutputAlreadyExists
	}

	sink, err := b.createStreamSink(url)
	if err != nil {
		return err
	}

	// add to bin
	if err = b.bin.AddMany(sink.queue, sink.sink); err != nil {
		return err
	}

	// link queue to sink
	if err = sink.queue.Link(sink.sink); err != nil {
		_ = b.bin.RemoveMany(sink.queue, sink.sink)
		return err
	}

	teeSrcPad := b.tee.GetRequestPad("src_%u")
	sink.pad = teeSrcPad.GetName()

	teeSrcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		// link tee to queue
		if linkReturn := pad.Link(sink.queue.GetStaticPad("sink")); linkReturn != gst.PadLinkOK {
			logger.Errorw("failed to link tee to queue", err)
		}

		// sync state
		sink.queue.SyncStateWithParent()
		sink.sink.SyncStateWithParent()

		return gst.PadProbeRemove
	})

	b.sinks[url] = sink
	return nil
}

func (b *streamOutputBin) RemoveSink(url string) error {
	sink, ok := b.sinks[url]
	if !ok {
		return errors.ErrOutputNotFound
	}

	srcPad := b.tee.GetStaticPad(sink.pad)
	srcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		// remove probe
		pad.RemoveProbe(uint64(info.ID()))

		// unlink queue
		pad.Unlink(sink.queue.GetStaticPad("sink"))

		// send EOS to queue
		sink.queue.GetStaticPad("sink").SendEvent(gst.NewEOSEvent())

		// remove from bin
		if err := b.bin.RemoveMany(sink.queue, sink.sink); err != nil {
			logger.Errorw("failed to remove rtmp queue", err)
		}
		if err := sink.queue.SetState(gst.StateNull); err != nil {
			logger.Errorw("failed stop rtmp queue", err)
		}
		if err := sink.sink.SetState(gst.StateNull); err != nil {
			logger.Errorw("failed to stop rtmp sink", err)
		}

		// release tee src pad
		b.tee.ReleaseRequestPad(pad)

		return gst.PadProbeOK
	})

	delete(b.sinks, url)
	return nil
}

func (b *streamOutputBin) RemoveSinkByName(name string) error {
	for url, sink := range b.sinks {
		if sink.queue.GetName() == name || sink.sink.GetName() == name {
			return b.RemoveSink(url)
		}
	}

	return errors.ErrOutputNotFound
}

func (b *streamOutputBin) Close() error {
	return nil
}
