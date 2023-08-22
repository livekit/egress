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
	"strings"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
)

func createGhostPads(src, sink *Bin) (*gst.GhostPad, *gst.GhostPad, error) {
	srcName := src.bin.GetName()
	sinkName := sink.bin.GetName()

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

	srcPrimary, srcSecondary, srcAny := getPadTemplates(srcElements, gst.PadDirectionSource)
	sinkPrimary, sinkSecondary, sinkAny := getPadTemplates(sinkElements, gst.PadDirectionSink)

	for srcCaps, srcTemplate := range srcPrimary {
		if sinkTemplate, ok := sinkPrimary[srcCaps]; ok {
			return getPad(srcElement, srcTemplate), getPad(sinkElement, sinkTemplate), nil
		}
	}
	for dataType, srcTemplate := range srcSecondary {
		if sinkTemplate, ok := sinkSecondary[dataType]; ok {
			return getPad(srcElement, srcTemplate), getPad(sinkElement, sinkTemplate), nil
		}
	}
	if srcAny != nil && sinkAny != nil {
		return getPad(srcElement, srcAny), getPad(sinkElement, sinkAny), nil
	}

	return nil, nil, errors.ErrGhostPadFailed
}

func getPad(e *gst.Element, template *gst.PadTemplate) *gst.Pad {
	if template.Presence() == gst.PadPresenceAlways {
		return e.GetStaticPad(template.Name())
	} else {
		return e.GetRequestPad(template.Name())
	}
}

func getPadTemplates(elements []*gst.Element, direction gst.PadDirection) (
	map[string]*gst.PadTemplate,
	map[string]*gst.PadTemplate,
	*gst.PadTemplate,
) {
	primary := make(map[string]*gst.PadTemplate)
	secondary := make(map[string]*gst.PadTemplate)
	var anyTemplate *gst.PadTemplate

	var i int
	if direction == gst.PadDirectionSource {
		i = len(elements) - 1
	}

	for i >= 0 && i < len(elements) {
		padTemplates := elements[i].GetPadTemplates()
		for _, padTemplate := range padTemplates {
			if padTemplate.Direction() == direction {
				caps := padTemplate.Caps()

				if caps.IsAny() {
					if strings.HasPrefix(padTemplate.Name(), direction.String()) {
						// most generic pad
						if anyTemplate == nil {
							anyTemplate = padTemplate
						} else {
							continue
						}
					} else {
						// any caps but associated name
						dataType := padTemplate.Name()
						if strings.HasSuffix(dataType, "_%u") {
							dataType = dataType[:len(dataType)-3]
						}
						if anyTemplate != nil {
							secondary[dataType] = anyTemplate
							return primary, secondary, nil
						}
						secondary[dataType] = padTemplate
					}
				} else {
					// specified caps
					splitCaps := strings.Split(caps.String(), ";")
					for _, c := range splitCaps {
						capsName := strings.SplitN(c, ",", 2)[0]
						dataType := strings.Split(capsName, "/")[0]
						if anyTemplate != nil {
							primary[capsName] = anyTemplate
							secondary[dataType] = anyTemplate
						} else {
							primary[capsName] = padTemplate
							secondary[dataType] = padTemplate
						}
					}
					if anyTemplate != nil {
						return primary, secondary, anyTemplate
					}
				}
			}
		}
		if anyTemplate == nil {
			for _, template := range primary {
				return primary, secondary, template
			}
			for _, template := range secondary {
				return primary, secondary, template
			}
			return primary, secondary, nil
		}

		if direction == gst.PadDirectionSource {
			i--
		} else {
			i++
		}
	}

	return primary, secondary, anyTemplate
}
