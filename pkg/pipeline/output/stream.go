package output

import (
	"fmt"
	"sync"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
)

type StreamOutput struct {
	*outputBase

	sync.RWMutex
	protocol types.OutputType

	mux   *gst.Element
	tee   *gst.Element
	sinks map[string]*streamSink
}

func (b *Bin) buildStreamOutput(p *config.PipelineConfig, out *config.OutputConfig) (*StreamOutput, error) {
	base, err := b.buildOutputBase(p)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	mux, err := buildStreamMux(out)
	if err != nil {
		return nil, err
	}

	// create elements
	tee, err := gst.NewElement("tee")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	if err := b.bin.AddMany(mux, tee); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	sinks := make(map[string]*streamSink)
	for _, url := range out.StreamUrls {
		sink, err := buildStreamSink(out.OutputType, url)
		if err != nil {
			return nil, err
		}

		if err = b.bin.AddMany(sink.queue, sink.sink); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		sinks[url] = sink
	}

	return &StreamOutput{
		outputBase: base,
		protocol:   out.OutputType,
		mux:        mux,
		tee:        tee,
		sinks:      sinks,
	}, nil
}

func buildStreamMux(out *config.OutputConfig) (*gst.Element, error) {
	switch out.OutputType {
	case types.OutputTypeRTMP:
		mux, err := gst.NewElement("flvmux")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("streamable", true); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("latency", uint64(1e8)); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		return mux, nil

	default:
		return nil, errors.ErrInvalidInput("output type")
	}
}

func buildStreamSink(protocol types.OutputType, url string) (*streamSink, error) {
	id := utils.NewGuid("")

	queue, err := gst.NewElementWithName("queue", fmt.Sprintf("queue_%s", id))
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	queue.SetArg("leaky", "downstream")

	var sink *gst.Element
	switch protocol {
	case types.OutputTypeRTMP:
		sink, err = gst.NewElementWithName("rtmp2sink", fmt.Sprintf("sink_%s", id))
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = sink.SetProperty("sync", false); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = sink.Set("location", url); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}

	return &streamSink{
		queue: queue,
		sink:  sink,
	}, nil
}

func (o *StreamOutput) Link() error {
	o.RLock()
	defer o.RUnlock()

	// link audio to mux
	if o.audioQueue != nil {
		if err := builder.LinkPads(
			"audio queue", o.audioQueue.GetStaticPad("src"),
			"stream mux", o.mux.GetRequestPad("audio"),
		); err != nil {
			return err
		}
	}

	// link video to mux
	if o.videoQueue != nil {
		if err := builder.LinkPads(
			"video queue", o.videoQueue.GetStaticPad("src"),
			"stream mux", o.mux.GetRequestPad("video"),
		); err != nil {
			return err
		}
	}

	// link mux to tee
	if err := o.mux.Link(o.tee); err != nil {
		return errors.ErrPadLinkFailed("mux", "tee", err.Error())
	}

	// link tee to sinks
	for _, sink := range o.sinks {
		if err := sink.link(o.tee, false); err != nil {
			return err
		}
	}

	return nil
}

func (o *StreamOutput) GetUrl(name string) (string, error) {
	o.RLock()
	defer o.RUnlock()

	for url, sink := range o.sinks {
		if sink.queue.GetName() == name || sink.sink.GetName() == name {
			return url, nil
		}
	}

	return "", errors.ErrStreamNotFound
}

func (o *StreamOutput) AddSink(bin *gst.Bin, url string) error {
	o.Lock()
	defer o.Unlock()

	if _, ok := o.sinks[url]; ok {
		return errors.ErrStreamAlreadyExists
	}

	sink, err := buildStreamSink(o.protocol, url)
	if err != nil {
		return err
	}

	// add to bin
	if err = bin.AddMany(sink.queue, sink.sink); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	// link queue to sink
	if err = sink.link(o.tee, true); err != nil {
		return err
	}

	o.sinks[url] = sink
	return nil
}

func (o *StreamOutput) RemoveSink(bin *gst.Bin, url string) error {
	o.Lock()
	defer o.Unlock()

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
		if err := bin.RemoveMany(sink.queue, sink.sink); err != nil {
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

type streamSink struct {
	pad   string
	queue *gst.Element
	sink  *gst.Element
}

func (o *streamSink) link(tee *gst.Element, live bool) error {
	sinkPad := o.sink.GetStaticPad("sink")

	proxy := gst.NewGhostPad("proxy", sinkPad)

	// proxy isn't saved/stored anywhere, so we need to call ref
	// pad gets released in RemoveSink
	proxy.Ref()

	// intercept FlowFlushing from rtmp2sink
	proxy.SetChainFunction(func(self *gst.Pad, _ *gst.Object, buffer *gst.Buffer) gst.FlowReturn {
		// buffer gets automatically unreferenced by go-gst
		// without referencing it here, it will sometimes be garbage collected before being written
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
	if err := builder.LinkPads("queue", o.queue.GetStaticPad("src"), "proxy", proxy.Pad); err != nil {
		return err
	}

	// get tee pad
	pad := tee.GetRequestPad("src_%u")
	o.pad = pad.GetName()

	if live {
		// use probe
		pad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			// link tee to queue
			if err := builder.LinkPads("tee", pad, "queue", o.queue.GetStaticPad("sink")); err != nil {
				logger.Errorw("failed to link tee to queue", err)
				return gst.PadProbeUnhandled
			}

			// sync state
			o.queue.SyncStateWithParent()
			o.sink.SyncStateWithParent()

			return gst.PadProbeRemove
		})
	} else {
		// link tee to queue
		if err := builder.LinkPads("tee", pad, "queue", o.queue.GetStaticPad("sink")); err != nil {
			return err
		}
	}

	return nil
}
