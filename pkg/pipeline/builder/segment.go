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

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
)

type FirstSampleMetadata struct {
	StartDate int64 // Real time date of the first media sample
}

const (
	tsMuxerFactory   = "mpegtsmux"
	fmp4MuxerFactory = "isofmp4mux"
)

// fmp4FragmentDuration keeps isofmp4mux from ending a fragment on its own: splitmuxsink
// ends each segment with EOS, and a duration far beyond any segment leaves that as the
// only boundary, so every segment holds exactly one fragment.
const fmp4FragmentDuration = 24 * time.Hour

// setSegmentMuxer picks the muxer splitmuxsink instantiates for each segment. fmp4
// segments are configured through muxer-added rather than muxer-properties, which
// splitmuxsink only applies when finalizing asynchronously.
func setSegmentMuxer(sink *gst.Element, o *config.SegmentConfig) error {
	if o.SegmentOutputType != types.OutputTypeM4S {
		if err := sink.SetProperty("muxer-factory", tsMuxerFactory); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		return nil
	}

	if err := sink.SetProperty("muxer-factory", fmp4MuxerFactory); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if _, err := sink.Connect("muxer-added", func(_ *gst.Element, muxer *gst.Element) {
		if err := configureFMP4Muxer(muxer); err != nil {
			logger.Errorw("failed to configure fmp4 muxer", err)
		}
	}); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	return nil
}

func configureFMP4Muxer(muxer *gst.Element) error {
	return muxer.SetProperty("fragment-duration", uint64(fmp4FragmentDuration))
}

func BuildSegmentBin(pipeline *gstreamer.Pipeline, p *config.PipelineConfig) (*gstreamer.Bin, error) {
	b := pipeline.NewBin("segment")
	o := p.GetSegmentConfig()

	var h264ParseFixer *ptsFixer

	var err error
	if p.VideoEnabled {
		h264ParseFixer, err = newPTSFixer("h264parse", "segment:h264")
		if err != nil {
			return nil, err
		}

		if err = b.AddElements(h264ParseFixer.Element); err != nil {
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

	if err = setSegmentMuxer(sink, o); err != nil {
		return nil, err
	}

	segmentExt := string(types.FileExtensionForOutputType[o.SegmentOutputType])

	var startDate time.Time
	_, err = sink.Connect("format-location-full", func(_ *gst.Element, fragmentId uint, firstSample *gst.Sample) string {
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
			segmentName = fmt.Sprintf("%s_%s%03d%s", o.SegmentPrefix, ts.Format("20060102150405"), ts.UnixMilli()%1000, segmentExt)
		default:
			segmentName = fmt.Sprintf("%s_%05d%s", o.SegmentPrefix, fragmentId, segmentExt)
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
		if name == audioBinName {
			return sink.GetRequestPad("audio_%u")
		} else if h264ParseFixer != nil {
			return h264ParseFixer.GetStaticPad("sink")
		}
		// Should never happen
		return nil

	})

	return b, nil
}
