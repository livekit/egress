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
)

func createGhostPads(src, sink *Bin) (*gst.GhostPad, *gst.GhostPad, error) {
	srcName := src.bin.GetName()
	sinkName := sink.bin.GetName()

	// logger.Debugw(fmt.Sprintf("linking %s to %s", srcName, sinkName))

	srcPad, sinkPad, err := getPads(src.elements, sink.elements)
	if err != nil {
		return nil, nil, err
	}

	srcGhostPad := gst.NewGhostPad(fmt.Sprintf("%s_%s_sink", srcName, sinkName), srcPad)
	src.pads[sinkName] = srcGhostPad
	src.bin.AddPad(srcGhostPad.Pad)

	sinkGhostPad := gst.NewGhostPad(fmt.Sprintf("%s_%s_src", srcName, sinkName), sinkPad)
	sink.pads[srcName] = sinkGhostPad
	sink.bin.AddPad(sinkGhostPad.Pad)

	return srcGhostPad, sinkGhostPad, nil
}

func createGhostPadsWithQueue(src, sink *Bin, queue *gst.Element) (*gst.GhostPad, *gst.GhostPad, error) {
	srcName := src.bin.GetName()
	sinkName := sink.bin.GetName()

	// logger.Debugw(fmt.Sprintf("linking %s to %s", srcName, sinkName))

	srcPad, sinkPad, err := getPads(src.elements, sink.elements)
	if err != nil {
		return nil, nil, err
	}
	if padReturn := queue.GetStaticPad("src").Link(sinkPad); padReturn != gst.PadLinkOK {
		return nil, nil, errors.ErrPadLinkFailed(queue.GetName(), sinkName, padReturn.String())
	}

	srcGhostPad := gst.NewGhostPad(fmt.Sprintf("%s_%s_sink", srcName, sinkName), srcPad)
	src.pads[sinkName] = srcGhostPad
	src.bin.AddPad(srcGhostPad.Pad)

	sinkGhostPad := gst.NewGhostPad(fmt.Sprintf("%s_%s_src", srcName, sinkName), queue.GetStaticPad("sink"))
	sink.pads[srcName] = sinkGhostPad
	sink.bin.AddPad(sinkGhostPad.Pad)

	return srcGhostPad, sinkGhostPad, nil
}

func getGhostPads(src, sink *Bin) (*gst.GhostPad, *gst.GhostPad) {
	srcPad := src.pads[sink.bin.GetName()]
	sinkPad := sink.pads[src.bin.GetName()]

	delete(src.pads, sink.bin.GetName())
	delete(sink.pads, src.bin.GetName())

	return srcPad, sinkPad
}

func getPads(srcElements, sinkElements []*gst.Element) (*gst.Pad, *gst.Pad, error) {
	srcIdx := len(srcElements) - 1
	sinkIdx := 0

	srcElement := srcElements[srcIdx]
	sinkElement := sinkElements[sinkIdx]

	srcTypes := getPadTemplates(srcElements, gst.PadDirectionSource)
	sinkTypes := getPadTemplates(sinkElements, gst.PadDirectionSink)

	for srcCaps, srcTemplate := range srcTypes {
		for sinkCaps, sinkTemplate := range sinkTypes {
			if srcCaps.CanIntersect(sinkCaps) {
				// logger.Infow(fmt.Sprintf(
				// 	"linking %s to %s", srcElement.GetName(), sinkElement.GetName()),
				// 	"src", srcTemplate.Name(),
				// 	"sink", sinkTemplate.Name(),
				// )
				var srcPad, sinkPad *gst.Pad
				if srcTemplate.Presence() == gst.PadPresenceAlways {
					srcPad = srcElement.GetStaticPad(srcTemplate.Name())
				} else {
					srcPad = srcElement.GetRequestPad(srcTemplate.Name())
				}
				if sinkTemplate.Presence() == gst.PadPresenceAlways {
					sinkPad = sinkElement.GetStaticPad(sinkTemplate.Name())
				} else {
					sinkPad = sinkElement.GetRequestPad(sinkTemplate.Name())
				}
				return srcPad, sinkPad, nil
			}
		}
	}

	return nil, nil, errors.ErrGhostPadFailed
}

func getPadTemplates(elements []*gst.Element, direction gst.PadDirection) map[*gst.Caps]*gst.PadTemplate {
	templates := make(map[*gst.Caps]*gst.PadTemplate)

	var anyCaps *gst.Caps
	var anyTemplate *gst.PadTemplate

	var i int
	if direction == gst.PadDirectionSource {
		i = len(elements) - 1
	}

	firstLoop := true
	for i >= 0 && i < len(elements) {
		padTemplates := elements[i].GetPadTemplates()
		for _, padTemplate := range padTemplates {
			if padTemplate.Direction() == direction {
				caps := padTemplate.Caps()
				if firstLoop {
					if caps.IsAny() || caps.IsEmpty() {
						anyTemplate = padTemplate
						anyCaps = caps
						break
					} else {
						templates[caps] = padTemplate
					}
				} else {
					if caps.IsAny() || caps.IsEmpty() {
						break
					} else {
						templates[caps] = anyTemplate
						return templates
					}
				}
			}
		}

		if anyTemplate == nil {
			return templates
		} else {
			firstLoop = false
		}

		if direction == gst.PadDirectionSource {
			i--
		} else {
			i++
		}
	}
	if anyTemplate != nil {
		templates[anyCaps] = anyTemplate
	}

	return templates
}
