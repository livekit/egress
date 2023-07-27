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

package builder

import (
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
)

type pad interface {
	Link(*gst.Pad) gst.PadLinkReturn
}

func LinkPads(src string, srcPad pad, sink string, sinkPad *gst.Pad) error {
	if srcPad == nil {
		return errors.ErrPadLinkFailed(src, sink, fmt.Sprintf("missing %s pad", src))
	}
	if sinkPad == nil {
		return errors.ErrPadLinkFailed(src, sink, fmt.Sprintf("missing %s pad", sink))
	}
	if linkReturn := srcPad.Link(sinkPad); linkReturn != gst.PadLinkOK {
		return errors.ErrPadLinkFailed(src, sink, linkReturn.String())
	}
	return nil
}

func BuildQueue(name string, latency uint64, leaky bool) (*gst.Element, error) {
	queue, err := gst.NewElementWithName("queue", name)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = queue.SetProperty("max-size-time", latency); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = queue.SetProperty("max-size-bytes", uint(0)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = queue.SetProperty("max-size-buffers", uint(0)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if leaky {
		queue.SetArg("leaky", "downstream")
	}

	return queue, nil
}

func GetSrcPad(elements []*gst.Element) *gst.Pad {
	return elements[len(elements)-1].GetStaticPad("src")
}
