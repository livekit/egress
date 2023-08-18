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

package sink

import (
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/protocol/logger"
)

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
	if err := gstreamer.LinkPads("queue", o.queue.GetStaticPad("src"), "proxy", proxy.Pad); err != nil {
		return err
	}

	// get tee pad
	pad := tee.GetRequestPad("src_%u")
	o.pad = pad.GetName()

	if live {
		// probe required to add a sink to an active pipeline
		pad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			// link tee to queue
			if err := gstreamer.LinkPads("tee", pad, "queue", o.queue.GetStaticPad("sink")); err != nil {
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
		if err := gstreamer.LinkPads("tee", pad, "queue", o.queue.GetStaticPad("sink")); err != nil {
			return err
		}
	}

	return nil
}
