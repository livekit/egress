package output

import (
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
)

type Bin struct {
	bin *gst.Bin

	// stream
	protocol params.OutputType
	tee      *gst.Element
	sinks    map[string]*streamSink

	logger logger.Logger
}

type streamSink struct {
	pad   string
	queue *gst.Element
	sink  *gst.Element
}

func Build(p *params.Params) (*Bin, error) {
	switch p.EgressType {
	case params.EgressTypeFile:
		return buildFileOutputBin(p)
	case params.EgressTypeStream:
		return buildStreamOutputBin(p)
	case params.EgressTypeWebsocket:
		return buildWebsocketOutputBin(p)
	case params.EgressTypeSegmentedFile:
		// In the case of segmented output, the muxer and the sink are embedded in the same object.
		return nil, nil
	default:
		return nil, errors.ErrInvalidInput("egress type")
	}
}

func (b *Bin) Element() *gst.Element {
	return b.bin.Element
}

func (b *Bin) Link() error {
	// stream tee and sinks
	for _, sink := range b.sinks {
		// link queue to rtmp sink
		if err := sink.queue.Link(sink.sink); err != nil {
			return err
		}

		pad := b.tee.GetRequestPad("src_%u")
		sink.pad = pad.GetName()

		// link tee to queue
		if linkReturn := pad.Link(sink.queue.GetStaticPad("sink")); linkReturn != gst.PadLinkOK {
			return errors.ErrPadLinkFailed("tee", linkReturn.String())
		}
	}

	return nil
}

func (b *Bin) AddSink(url string) error {
	if _, ok := b.sinks[url]; ok {
		return errors.ErrStreamAlreadyExists
	}

	sink, err := buildStreamSink(b.protocol, url)
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
			b.logger.Errorw("failed to link tee to queue", err)
		}

		// sync state
		sink.queue.SyncStateWithParent()
		sink.sink.SyncStateWithParent()

		return gst.PadProbeRemove
	})

	b.sinks[url] = sink
	return nil
}

func (b *Bin) RemoveSink(url string) error {
	sink, ok := b.sinks[url]
	if !ok {
		return errors.ErrStreamNotFound
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
			b.logger.Errorw("failed to remove rtmp queue", err)
		}
		if err := sink.queue.SetState(gst.StateNull); err != nil {
			b.logger.Errorw("failed stop rtmp queue", err)
		}
		if err := sink.sink.SetState(gst.StateNull); err != nil {
			b.logger.Errorw("failed to stop rtmp sink", err)
		}

		// release tee src pad
		b.tee.ReleaseRequestPad(pad)

		return gst.PadProbeOK
	})

	delete(b.sinks, url)
	return nil
}

func (b *Bin) RemoveSinkByName(name string) (string, error) {
	for url, sink := range b.sinks {
		if sink.queue.GetName() == name || sink.sink.GetName() == name {
			return url, b.RemoveSink(url)
		}
	}

	return "", errors.ErrStreamNotFound
}
