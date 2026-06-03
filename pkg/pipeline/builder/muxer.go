// Copyright 2025 LiveKit, Inc.
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
	"strings"

	"github.com/go-gst/go-gst/gst"
)

// muxer captures the minimal behavior builders need from a muxing element, allowing
// us to swap between real gst muxers and light-weight shims (e.g. xingmux for MP3).
type muxer interface {
	GetRequestPad(name string) *gst.Pad
	GetElement() *gst.Element
}

// muxerImpl wraps a concrete gst.Element so it satisfies the muxer interface.
type muxerImpl struct {
	*gst.Element
}

// newMuxer constructs a wrapper around the named gst muxer element.
func newMuxer(elementName string) (*muxerImpl, error) {
	element, err := gst.NewElement(elementName)
	if err != nil {
		return nil, err
	}
	if factory := element.GetFactory(); factory != nil {
		if klass := factory.GetMetadata("klass"); !strings.Contains(klass, "Muxer") {
			element.Unref()
			return nil, fmt.Errorf("element %s is not a muxer", elementName)
		}
	}
	return &muxerImpl{
		Element: element,
	}, nil
}

func (m *muxerImpl) GetRequestPad(name string) *gst.Pad {
	return m.Element.GetRequestPad(name)
}

func (m *muxerImpl) GetElement() *gst.Element {
	return m.Element
}

// mp3Muxer wraps xingmux as a muxer so audio-only MP3 outputs
// can reuse the same linking logic as containerised formats.
type mp3Muxer struct {
	muxerImpl
}

// newMP3Muxer provides a muxer-compatible wrapper around gst xingmux.
// xingmux inserts a Xing header containing total frame and byte counts,
// allowing players to determine the file duration without scanning every frame.
func newMP3Muxer() (*mp3Muxer, error) {
	xing, err := gst.NewElement("xingmux")
	if err != nil {
		return nil, err
	}
	return &mp3Muxer{
		muxerImpl: muxerImpl{
			Element: xing,
		},
	}, nil
}

// GetRequestPad always returns the static sink pad to satisfy the muxer contract.
func (m *mp3Muxer) GetRequestPad(_ string) *gst.Pad {
	return m.GetStaticPad("sink")
}
