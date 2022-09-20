package output

import (
	"context"
	"sync"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

type Bin struct {
	bin *gst.Bin

	// stream
	protocol params.OutputType
	tee      *gst.Element
	sinks    map[string]*streamSink
	lock     sync.Mutex

	logger logger.Logger
}

type streamSink struct {
	pad   string
	queue *gst.Element
	sink  *gst.Element
}

func Build(ctx context.Context, p *params.Params) (*Bin, error) {
	ctx, span := tracer.Start(ctx, "Output.Build")
	defer span.End()

	switch p.EgressType {
	case params.EgressTypeFile:
		return buildFileOutputBin(p)
	case params.EgressTypeStream:
		return buildStreamOutputBin(p)
	case params.EgressTypeWebsocket:
		return buildWebsocketOutputBin(p)
	case params.EgressTypeSegmentedFile:
		// In the case of segmented output, the muxer and the sink are embedded in the same object
		return nil, nil
	default:
		return nil, errors.ErrInvalidInput("egress type")
	}
}

func (b *Bin) Element() *gst.Element {
	return b.bin.Element
}

func (b *Bin) Link() error {
	b.lock.Lock()
	defer b.lock.Unlock()

	// stream tee and sinks
	for _, sink := range b.sinks {
		// link queue to sink
		if err := b.linkSink(sink); err != nil {
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

func (b *Bin) linkSink(sink *streamSink) error {
	sinkPad := sink.sink.GetStaticPad("sink")

	proxy := gst.NewGhostPad("proxy", sinkPad)
	// proxy isn't saved/stored anywhere, so we need to call ref
	proxy.Ref()

	// intercept FlowFlushing from rtmp2sink
	proxy.SetChainFunction(func(self *gst.Pad, _ *gst.Object, buffer *gst.Buffer) gst.FlowReturn {
		buffer.Ref()

		internal, _ := self.GetInternalLinks()
		if len(internal) != 1 {
			// there should always be exactly one
			b.logger.Errorw("unexpected internal links", nil, "links", len(internal))
			return gst.FlowNotLinked
		}

		// push buffer to rtmp2sink sink pad
		flow := internal[0].Push(buffer)
		if flow == gst.FlowFlushing {
			// replace with ok - pipeline should continue and this sink will be removed
			return gst.FlowOK
		}
		return flow
	})
	proxy.ActivateMode(gst.PadModePush, true)

	// link
	if linkReturn := sink.queue.GetStaticPad("src").Link(proxy.Pad); linkReturn != gst.PadLinkOK {
		return errors.ErrPadLinkFailed("rtmp sink", linkReturn.String())
	}

	return nil
}

func (b *Bin) AddSink(url string) error {
	b.lock.Lock()
	defer b.lock.Unlock()

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
	if err = b.linkSink(sink); err != nil {
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
	b.lock.Lock()
	defer b.lock.Unlock()

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
			b.logger.Errorw("failed to remove rtmp sink", err)
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

func (b *Bin) GetUrlFromName(name string) (string, error) {
	for url, sink := range b.sinks {
		if sink.queue.GetName() == name || sink.sink.GetName() == name {
			return url, nil
		}
	}

	return "", errors.ErrStreamNotFound
}
