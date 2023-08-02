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

func (b *Bin) buildStreamOutput(p *config.PipelineConfig) (*StreamOutput, error) {
	o := p.GetStreamConfig()

	base, err := b.buildOutputBase(p, types.EgressTypeStream)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	mux, err := buildStreamMux(p, o)
	if err != nil {
		return nil, err
	}

	// create elements
	tee, err := gst.NewElement("tee")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = tee.SetProperty("allow-not-linked", true); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	if err = b.bin.AddMany(mux, tee); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	sinks := make(map[string]*streamSink)
	for _, url := range o.Urls {
		sink, err := buildStreamSink(o.OutputType, url)
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
		protocol:   o.OutputType,
		mux:        mux,
		tee:        tee,
		sinks:      sinks,
	}, nil
}

func buildStreamMux(p *config.PipelineConfig, o *config.StreamConfig) (*gst.Element, error) {
	switch o.OutputType {
	case types.OutputTypeRTMP:
		mux, err := gst.NewElement("flvmux")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("streamable", true); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("skip-backwards-streams", true); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		// add latency to give time for flvmux to receive and order packets from both streams
		if err = mux.SetProperty("latency", p.Latency); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		return mux, nil

	default:
		return nil, errors.ErrInvalidInput("output type")
	}
}

func buildStreamSink(protocol types.OutputType, url string) (*streamSink, error) {
	id := utils.NewGuid("")

	queue, err := gst.NewElementWithName("queue", fmt.Sprintf("stream_queue_%s", id))
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	queue.SetArg("leaky", "downstream")

	var sink *gst.Element
	switch protocol {
	case types.OutputTypeRTMP:
		sink, err = gst.NewElementWithName("rtmp2sink", fmt.Sprintf("rtmp2sink_%s", id))
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = sink.SetProperty("sync", false); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = sink.SetProperty("async-connect", false); err != nil {
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

	return "", errors.ErrStreamNotFound(name)
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

func (o *StreamOutput) Reset(name string, streamErr error) (bool, error) {
	o.RLock()
	defer o.RUnlock()

	var url string
	var sink *streamSink
	for u, s := range o.sinks {
		if s.queue.GetName() == name || s.sink.GetName() == name {
			url = u
			sink = s
			break
		}
	}
	if sink == nil {
		return false, errors.ErrStreamNotFound(name)
	}

	sink.reconnections++
	if sink.reconnections > 3 {
		return false, nil
	}

	redacted, _ := utils.RedactStreamKey(url)
	logger.Warnw("resetting stream", streamErr, "url", redacted)

	if err := sink.sink.BlockSetState(gst.StateNull); err != nil {
		return false, err
	}
	if err := sink.sink.BlockSetState(gst.StatePlaying); err != nil {
		return false, err
	}

	return true, nil
}

func (o *StreamOutput) RemoveSink(bin *gst.Bin, url string) error {
	o.Lock()
	defer o.Unlock()

	sink, ok := o.sinks[url]
	if !ok {
		return errors.ErrStreamNotFound(url)
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
	pad           string
	queue         *gst.Element
	sink          *gst.Element
	reconnections int
}

func (o *streamSink) link(tee *gst.Element, live bool) error {
	sinkPad := o.sink.GetStaticPad("sink")

	proxy := gst.NewGhostPad("proxy", sinkPad)

	// Proxy isn't saved/stored anywhere, so we need to call ref.
	// It is later released in RemoveSink
	proxy.Ref()

	// Intercept flows from rtmp2sink. Anything besides EOS will be ignored
	proxy.SetChainFunction(func(self *gst.Pad, _ *gst.Object, buffer *gst.Buffer) gst.FlowReturn {
		// Buffer gets automatically unreferenced by go-gst.
		// Without referencing it here, it will sometimes be garbage collected before being written
		buffer.Ref()

		internal, _ := self.GetInternalLinks()
		if len(internal) != 1 {
			// there should always be exactly one
			logger.Errorw("unexpected internal links", nil, "links", len(internal))
			return gst.FlowNotLinked
		}

		// push buffer to rtmp2sink sink pad
		if internal[0].Push(buffer) == gst.FlowEOS {
			return gst.FlowEOS
		}

		return gst.FlowOK
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
		// probe required to add a sink to an active pipeline
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
