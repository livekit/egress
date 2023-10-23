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
	"path"
	"time"

	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type FirstSampleMetadata struct {
	StartDate int64 // Real time date of the first media sample
}

func BuildSegmentBin(pipeline *gstreamer.Pipeline, p *config.PipelineConfig) (*gstreamer.Bin, error) {
	b := pipeline.NewBin("segment")
	o := p.GetSegmentConfig()

	var h264parse *gst.Element
	var err error
	if p.VideoEnabled {
		h264parse, err = gst.NewElement("h264parse")
		if err != nil {
			return nil, err
		}

		if err = b.AddElements(h264parse); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}

	sink, err := gst.NewElement("splitmuxsink")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("max-size-time", uint64(time.Duration(o.SegmentDuration)*time.Second)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("send-keyframe-requests", true); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("muxer-factory", "mpegtsmux"); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	var startDate time.Time
	_, err = sink.Connect("format-location-full", func(self *gst.Element, fragmentId uint, firstSample *gst.Sample) string {
		var pts time.Duration
		if firstSample != nil && firstSample.GetBuffer() != nil {
			pts = *firstSample.GetBuffer().PresentationTimestamp().AsDuration()
		} else {
			logger.Infow("nil sample passed into 'format-location-full' event handler, assuming 0 pts")
		}

		if startDate.IsZero() {
			now := time.Now()

			startDate = now.Add(-pts)

			mdata := FirstSampleMetadata{
				StartDate: now.UnixNano(),
			}
			str := gst.MarshalStructure(mdata)
			msg := gst.NewElementMessage(sink, str)
			sink.GetBus().Post(msg)
		}

		var segmentName string
		switch o.SegmentSuffix {
		case livekit.SegmentedFileSuffix_TIMESTAMP:
			ts := startDate.Add(pts)
			segmentName = fmt.Sprintf("%s_%s%03d.ts", o.SegmentPrefix, ts.Format("20060102150405"), ts.UnixMilli()%1000)
		default:
			segmentName = fmt.Sprintf("%s_%05d.ts", o.SegmentPrefix, fragmentId)
		}
		return path.Join(o.LocalDir, segmentName)
	})
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	if err = b.AddElements(sink); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	b.SetGetSrcPad(func(name string) *gst.Pad {
		if name == "audio" {
			return sink.GetRequestPad("audio_%u")
		} else if h264parse != nil {
			return h264parse.GetStaticPad("sink")
		} else {
			// Should never happen
			return nil
		}
	})

	return b, nil
}
