package output

import (
	"context"
	"sync"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

type OutputBin struct {
	bin *gst.Bin

	// stream
	protocol types.OutputType
	tee      *gst.Element
	sinks    map[string]*streamSink
	lock     sync.Mutex
}

type streamSink struct {
	pad   string
	queue *gst.Element
	sink  *gst.Element
}

func New(ctx context.Context, p *config.PipelineConfig) (*OutputBin, error) {
	ctx, span := tracer.Start(ctx, "OutputBin.New")
	defer span.End()

	switch p.EgressType {
	case types.EgressTypeFile:
		return buildFileOutputBin(p)
	case types.EgressTypeStream:
		return buildStreamOutputBin(p)
	case types.EgressTypeWebsocket:
		return buildWebsocketOutputBin(p)
	case types.EgressTypeSegmentedFile:
		// In the case of segmented output, the muxer and the sink are embedded in the same object
		return nil, nil
	default:
		return nil, errors.ErrInvalidInput("egress type")
	}
}

func (o *OutputBin) Element() *gst.Element {
	return o.bin.Element
}

func (o *OutputBin) Link() error {
	o.lock.Lock()
	defer o.lock.Unlock()

	// stream tee and sinks
	for _, sink := range o.sinks {
		// link queue to sink
		if err := o.linkSink(sink); err != nil {
			return err
		}

		pad := o.tee.GetRequestPad("src_%u")
		sink.pad = pad.GetName()

		// link tee to queue
		if linkReturn := pad.Link(sink.queue.GetStaticPad("sink")); linkReturn != gst.PadLinkOK {
			return errors.ErrPadLinkFailed("sink", "tee", linkReturn.String())
		}
	}

	return nil
}

func (o *OutputBin) linkSink(sink *streamSink) error {
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
			logger.Errorw("unexpected internal links", nil, "links", len(internal))
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
		return errors.ErrPadLinkFailed("queue", "proxy", linkReturn.String())
	}

	return nil
}

func (o *OutputBin) AddSink(url string) error {
	o.lock.Lock()
	defer o.lock.Unlock()

	if _, ok := o.sinks[url]; ok {
		return errors.ErrStreamAlreadyExists
	}

	sink, err := buildStreamSink(o.protocol, url)
	if err != nil {
		return err
	}

	// add to bin
	if err = o.bin.AddMany(sink.queue, sink.sink); err != nil {
		return err
	}

	// link queue to sink
	if err = o.linkSink(sink); err != nil {
		return err
	}

	teeSrcPad := o.tee.GetRequestPad("src_%u")
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

	o.sinks[url] = sink
	return nil
}

func (o *OutputBin) RemoveSink(url string) error {
	o.lock.Lock()
	defer o.lock.Unlock()

	sink, ok := o.sinks[url]
	if !ok {
		return errors.ErrStreamNotFound
	}

	srcPad := o.tee.GetStaticPad(sink.pad)
	srcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		// remove probe
		pad.RemoveProbe(uint64(info.ID()))

		// unlink queue
		pad.Unlink(sink.queue.GetStaticPad("sink"))

		// send EOS to queue
		sink.queue.GetStaticPad("sink").SendEvent(gst.NewEOSEvent())

		// remove from bin
		if err := o.bin.RemoveMany(sink.queue, sink.sink); err != nil {
			logger.Errorw("failed to remove rtmp sink", err)
		}
		if err := sink.queue.SetState(gst.StateNull); err != nil {
			logger.Errorw("failed stop rtmp queue", err)
		}
		if err := sink.sink.SetState(gst.StateNull); err != nil {
			logger.Errorw("failed to stop rtmp sink", err)
		}

		// release tee src pad
		o.tee.ReleaseRequestPad(pad)

		return gst.PadProbeOK
	})

	delete(o.sinks, url)
	return nil
}

func (o *OutputBin) GetUrlFromName(name string) (string, error) {
	for url, sink := range o.sinks {
		if sink.queue.GetName() == name || sink.sink.GetName() == name {
			return url, nil
		}
	}

	return "", errors.ErrStreamNotFound
}
