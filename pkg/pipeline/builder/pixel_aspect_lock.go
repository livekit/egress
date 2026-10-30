// Copyright 2026 LiveKit, Inc.
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
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
)

// newPixelAspectLock returns a capssetter that keeps the pixel-aspect-ratio of the parser's first caps.
// mp4mux refuses a pixel-aspect-ratio change on renegotiation, which a simulcast layer switch can cause.
func newPixelAspectLock(parser *gst.Element) (*gst.Element, error) {
	setter, err := gst.NewElement("capssetter")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	srcPad := parser.GetStaticPad("src")
	if srcPad == nil {
		return nil, errors.ErrGstPipelineError(newMissingPadError(parser.GetName(), "src"))
	}

	// caps events on one pad are serialized, so the probe state needs no locking
	var lockedPAR string
	var changeLogged bool
	srcPad.AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		event := info.GetEvent()
		if event == nil || event.Type() != gst.EventTypeCaps {
			return gst.PadProbeOK
		}

		par := "1/1"
		if s := event.ParseCaps().GetStructureAt(0); s != nil {
			if v, err := s.GetValue("pixel-aspect-ratio"); err == nil {
				if f, ok := v.(*gst.FractionValue); ok {
					par = f.String()
				}
			}
		}

		if lockedPAR != "" {
			if par != lockedPAR && !changeLogged {
				changeLogged = true
				logger.Infow("holding pixel aspect ratio", "parser", parser.GetName(), "locked", lockedPAR, "incoming", par)
			}
			return gst.PadProbeOK
		}

		if err := setter.SetProperty("caps", gst.NewCapsFromString("video/x-h264,pixel-aspect-ratio=(fraction)"+par)); err != nil {
			logger.Warnw("could not lock pixel aspect ratio", err, "parser", parser.GetName())
			return gst.PadProbeOK
		}
		lockedPAR = par
		return gst.PadProbeOK
	})

	return setter, nil
}
