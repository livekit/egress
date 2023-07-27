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
	"path"
	"time"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type SegmentOutput struct {
	*outputBase

	sink      *gst.Element
	h264parse *gst.Element

	startDate time.Time
}

type FirstSampleMetadata struct {
	StartDate int64 // Real time date of the first media sample
}

func (b *Bin) buildSegmentOutput(p *config.PipelineConfig) (*SegmentOutput, error) {
	o := p.GetSegmentConfig()

	s := &SegmentOutput{}

	base, err := b.buildOutputBase(p, types.EgressTypeSegments)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	h264parse, err := gst.NewElement("h264parse")
	if err != nil {
		return nil, err
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

	_, err = sink.Connect("format-location-full", func(self *gst.Element, fragmentId uint, firstSample *gst.Sample) string {
		var pts time.Duration
		if firstSample != nil && firstSample.GetBuffer() != nil {
			pts = firstSample.GetBuffer().PresentationTimestamp()
		} else {
			logger.Infow("nil sample passed into 'format-location-full' event handler, assuming 0 pts")
		}

		if s.startDate.IsZero() {
			now := time.Now()

			s.startDate = now.Add(-pts)

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
			ts := s.startDate.Add(pts)
			segmentName = fmt.Sprintf("%s_%s%03d.ts", o.SegmentPrefix, ts.Format("20060102150405"), ts.UnixMilli()%1000)
		default:
			segmentName = fmt.Sprintf("%s_%05d.ts", o.SegmentPrefix, fragmentId)
		}
		return path.Join(o.LocalDir, segmentName)
	})
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	if err = b.bin.AddMany(h264parse, sink); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	s.outputBase = base
	s.h264parse = h264parse
	s.sink = sink

	return s, nil
}

func (o *SegmentOutput) Link() error {
	// link audio to sink
	if o.audioQueue != nil {
		if err := builder.LinkPads(
			"audio queue", o.audioQueue.GetStaticPad("src"),
			"split mux", o.sink.GetRequestPad("audio_%u"),
		); err != nil {
			return err
		}
	}

	// link video to sink
	if o.videoQueue != nil {
		if err := o.videoQueue.Link(o.h264parse); err != nil {
			return errors.ErrPadLinkFailed("video queue", "h264parse", err.Error())
		}
		if err := builder.LinkPads(
			"h264parse", o.h264parse.GetStaticPad("src"),
			"split mux", o.sink.GetRequestPad("video"),
		); err != nil {
			return err
		}
	}

	return nil
}
