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

package gstreamer

import (
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
)

func createGhostPads(src, sink *Bin) (*gst.GhostPad, *gst.GhostPad, error) {
	srcElement := src.elements[len(src.elements)-1]
	sinkElement := sink.elements[0]

	return createGhostPadsWithElements(src, sink, srcElement, sinkElement, false)
}

func createQueueGhostPads(src, sink *Bin, queue *gst.Element) (*gst.GhostPad, *gst.GhostPad, error) {
	srcElement := src.elements[len(src.elements)-1]

	return createGhostPadsWithElements(src, sink, srcElement, queue, true)
}

func createGhostPadsWithElements(
	src, sink *Bin, srcElement, sinkElement *gst.Element, queue bool,
) (*gst.GhostPad, *gst.GhostPad, error) {

	var srcPad, sinkPad *gst.GhostPad
	if src.binType != BinTypeMuxed || sink.binType == BinTypeMuxed {
		srcPad = createGhostPad(src, sink, srcElement, "src")
	} else {
		srcPad = createGhostPad(src, sink, srcElement, string(sink.binType))
	}
	if sink.binType != BinTypeMuxed || src.binType == BinTypeMuxed || queue {
		sinkPad = createGhostPad(sink, src, sinkElement, "sink")
	} else {
		sinkPad = createGhostPad(sink, src, sinkElement, string(src.binType))
	}

	if srcPad == nil || sinkPad == nil || !src.bin.AddPad(srcPad.Pad) || !sink.bin.AddPad(sinkPad.Pad) {
		return nil, nil, errors.ErrGhostPadFailed
	}

	src.pads[sink.bin.GetName()] = srcPad
	sink.pads[src.bin.GetName()] = sinkPad

	return srcPad, sinkPad, nil
}

func createGhostPad(b, other *Bin, e *gst.Element, padFormat string) *gst.GhostPad {
	if pad := getPad(e, padFormat); pad != nil {
		logger.Debugw(fmt.Sprintf("creating %s_%s ghost pad", b.bin.GetName(), padFormat))
		return gst.NewGhostPad(fmt.Sprintf("%s_%s", b.bin.GetName(), other.bin.GetName()), pad)
	}
	return nil
}

func getPad(e *gst.Element, padFormat string) *gst.Pad {
	if pad := e.GetStaticPad(padFormat); pad != nil {
		return pad
	}
	return e.GetRequestPad(fmt.Sprintf("%s_%%u", padFormat))
}

func removeGhostPads(src, sink *Bin) (*gst.GhostPad, *gst.GhostPad, error) {
	var srcPad, sinkPad *gst.GhostPad

	srcPad = src.pads[sink.bin.GetName()]
	delete(src.pads, sink.bin.GetName())

	sinkPad = sink.pads[src.bin.GetName()]
	delete(sink.pads, src.bin.GetName())

	if srcPad == nil || sinkPad == nil {
		return nil, nil, errors.ErrGhostPadFailed
	}

	return srcPad, sinkPad, nil
}
