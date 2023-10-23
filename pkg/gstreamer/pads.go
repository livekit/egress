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

	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
)

type padTemplate struct {
	element   *gst.Element
	template  *gst.PadTemplate
	capsNames map[string]struct{}
	dataTypes map[string]struct{}
}

func (p *padTemplate) toPad() *gst.Pad {
	if p.template.Presence() == gst.PadPresenceAlways {
		return p.element.GetStaticPad(p.template.Name())
	} else {
		return p.element.GetRequestPad(p.template.Name())
	}
}

func (p *padTemplate) findDirectMatch(others []*padTemplate) *padTemplate {
	for _, other := range others {
		for capsName := range p.capsNames {
			if _, ok := other.capsNames[capsName]; ok {
				return other
			}
		}
		for dataType := range p.dataTypes {
			if _, ok := other.dataTypes[dataType]; ok {
				return other
			}
		}
	}
	return nil
}

func (p *padTemplate) findAnyMatch(others []*padTemplate) *padTemplate {
	for _, other := range others {
		if _, ok := p.dataTypes["ANY"]; ok {
			return other
		}
		if _, ok := other.dataTypes["ANY"]; ok {
			return other
		}
	}
	return nil
}

func createGhostPadsLocked(src, sink *Bin, queue *gst.Element) (*gst.GhostPad, *gst.GhostPad, error) {
	srcName := src.bin.GetName()
	sinkName := sink.bin.GetName()

	srcPad, sinkPad, err := matchPadsLocked(src, sink)
	if err != nil {
		return nil, nil, err
	}

	srcGhostPad := gst.NewGhostPad(fmt.Sprintf("%s_%s_sink", srcName, sinkName), srcPad)
	src.pads[sinkName] = srcGhostPad
	src.bin.AddPad(srcGhostPad.Pad)

	if queue != nil {
		if padReturn := queue.GetStaticPad("src").Link(sinkPad); padReturn != gst.PadLinkOK {
			return nil, nil, errors.ErrPadLinkFailed(queue.GetName(), sinkName, padReturn.String())
		}

		sinkGhostPad := gst.NewGhostPad(fmt.Sprintf("%s_%s_src", srcName, sinkName), queue.GetStaticPad("sink"))
		sink.pads[srcName] = sinkGhostPad
		sink.bin.AddPad(sinkGhostPad.Pad)
		return srcGhostPad, sinkGhostPad, nil
	}

	sinkGhostPad := gst.NewGhostPad(fmt.Sprintf("%s_%s_src", srcName, sinkName), sinkPad)
	sink.pads[srcName] = sinkGhostPad
	sink.bin.AddPad(sinkGhostPad.Pad)
	return srcGhostPad, sinkGhostPad, nil
}

func matchPadsLocked(src, sink *Bin) (*gst.Pad, *gst.Pad, error) {
	var srcPad, sinkPad *gst.Pad
	var srcTemplates, sinkTemplates []*padTemplate
	if src.getSinkPad != nil {
		srcPad = src.getSinkPad(sink.bin.GetName())
	} else {
		srcTemplates = src.getPadTemplatesLocked(gst.PadDirectionSource)
	}
	if sink.getSrcPad != nil {
		sinkPad = sink.getSrcPad(src.bin.GetName())
	} else {
		sinkTemplates = sink.getPadTemplatesLocked(gst.PadDirectionSink)
	}

	switch {
	case srcPad != nil && sinkPad != nil:
		return srcPad, sinkPad, nil
	case srcPad != nil && len(sinkTemplates) == 1:
		return srcPad, sinkTemplates[0].toPad(), nil
	case sinkPad != nil && len(srcTemplates) == 1:
		return srcTemplates[0].toPad(), sinkPad, nil
	case len(srcTemplates) >= 1 && len(srcTemplates) >= 1:
		for _, srcTemplate := range srcTemplates {
			if sinkTemplate := srcTemplate.findDirectMatch(sinkTemplates); sinkTemplate != nil {
				return srcTemplate.toPad(), sinkTemplate.toPad(), nil
			}
		}
		for _, srcTemplate := range srcTemplates {
			if sinkTemplate := srcTemplate.findAnyMatch(sinkTemplates); sinkTemplate != nil {
				return srcTemplate.toPad(), sinkTemplate.toPad(), nil
			}
		}
	}

	logger.Warnw("could not match pads", nil, "srcTemplates", srcTemplates, "sinkTemplates", sinkTemplates)
	return nil, nil, errors.ErrGhostPadFailed
}

func (b *Bin) getPadTemplatesLocked(direction gst.PadDirection) []*padTemplate {
	var element *gst.Element
	if direction == gst.PadDirectionSource {
		element = b.elements[len(b.elements)-1]
	} else {
		element = b.elements[0]
	}

	allTemplates := element.GetPadTemplates()
	templates := make([]*padTemplate, 0)

	for _, template := range allTemplates {
		if template.Direction() == direction {
			t := &padTemplate{
				element:   element,
				template:  template,
				capsNames: make(map[string]struct{}),
				dataTypes: make(map[string]struct{}),
			}

			caps := template.Caps()
			if caps.IsAny() {
				if strings.HasPrefix(template.Name(), direction.String()) {
					// src/src_%u/sink/sink_%u pad
					capsNames, dataTypes, ok := b.getTypesLocked(direction)
					if ok {
						t.capsNames = capsNames
						t.dataTypes = dataTypes
					} else {
						t.dataTypes["ANY"] = struct{}{}
					}
				} else {
					// audio/audio_%u/video/video_%u pad
					dataType := template.Name()
					if strings.HasSuffix(dataType, "_%u") {
						dataType = dataType[:len(dataType)-3]
					}
					t.dataTypes[dataType] = struct{}{}
				}
			} else {
				// pad has caps
				splitCaps := strings.Split(caps.String(), "; ")
				for _, c := range splitCaps {
					capsName := strings.SplitN(c, ",", 2)[0]
					t.capsNames[capsName] = struct{}{}
					t.dataTypes[strings.Split(capsName, "/")[0]] = struct{}{}
				}
			}

			templates = append(templates, t)
		}
	}

	return templates
}

func (b *Bin) getTypesLocked(direction gst.PadDirection) (map[string]struct{}, map[string]struct{}, bool) {
	var i int
	if direction == gst.PadDirectionSource {
		i = len(b.elements) - 1
	}

	for i >= 0 && i < len(b.elements) {
		allTemplates := b.elements[i].GetPadTemplates()
		for _, template := range allTemplates {
			if template.Direction() == gst.PadDirectionSource {
				if caps := template.Caps(); !caps.IsAny() {
					capsNames := make(map[string]struct{})
					dataTypes := make(map[string]struct{})
					splitCaps := strings.Split(caps.String(), ";")
					for _, c := range splitCaps {
						capsName := strings.SplitN(c, ",", 2)[0]
						capsNames[capsName] = struct{}{}
						dataTypes[strings.Split(capsName, "/")[0]] = struct{}{}
					}
					return capsNames, dataTypes, true
				}
			}
		}

		if direction == gst.PadDirectionSource {
			i--
		} else {
			i++
		}
	}

	if direction == gst.PadDirectionSource {
		for _, src := range b.srcs {
			src.mu.Lock()
			capsNames, dataTypes, ok := src.getTypesLocked(direction)
			src.mu.Unlock()
			if ok {
				return capsNames, dataTypes, true
			}
		}
	} else {
		for _, sink := range b.sinks {
			sink.mu.Lock()
			capsNames, dataTypes, ok := sink.getTypesLocked(direction)
			sink.mu.Unlock()
			if ok {
				return capsNames, dataTypes, true
			}
		}
	}

	return nil, nil, false
}
