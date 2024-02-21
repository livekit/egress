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
	"github.com/livekit/egress/pkg/types"
)

const (
	imageQueueLatency = uint64(200 * time.Millisecond)
)

func BuildImageBins(pipeline *gstreamer.Pipeline, p *config.PipelineConfig) ([]*gstreamer.Bin, error) {
	o := p.GetImageConfigs()

	var bins []*gstreamer.Bin
	for _, c := range o {
		b, err := BuildImageBin(c, pipeline, p)
		if err != nil {
			return nil, err
		}

		bins = append(bins, b)
	}

	return bins, nil
}

func BuildImageBin(c *config.ImageConfig, pipeline *gstreamer.Pipeline, p *config.PipelineConfig) (*gstreamer.Bin, error) {
	b := pipeline.NewBin(fmt.Sprintf("image_%s", c.Id))

	var err error
	var fakeAudio *gst.Element
	if p.AudioEnabled {
		fakeAudio, err = gst.NewElement("fakesink")
		if err != nil {
			return nil, err
		}
	}

	queue, err := gstreamer.BuildQueue(fmt.Sprintf("image_queue_%s", c.Id), imageQueueLatency, true)
	if err != nil {
		return nil, err
	}
	if err := b.AddElements(queue); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	b.SetGetSrcPad(func(name string) *gst.Pad {
		if name == "audio" {
			return fakeAudio.GetStaticPad("sink")
		} else {
			return queue.GetStaticPad("sink")
		}
	})

	videoRate, err := gst.NewElement("videorate")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = videoRate.SetProperty("max-duplication-time", uint64(time.Duration(c.CaptureInterval)*time.Second)); err != nil {
		return nil, err
	}
	if err = videoRate.SetProperty("skip-to-first", true); err != nil {
		return nil, err
	}
	if err := b.AddElements(videoRate); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	videoScale, err := gst.NewElement("videoscale")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err := b.AddElements(videoScale); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	err = caps.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
		"video/x-raw,framerate=1/%d,format=I420,width=%d,height=%d,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1",
		c.CaptureInterval, c.Width, c.Height)))
	if err != nil {
		return nil, err
	}
	if err := b.AddElements(caps); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	switch c.ImageOutCodec {
	case types.MimeTypeJPEG:
		enc, err := gst.NewElement("jpegenc")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err := b.AddElements(enc); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	default:
		return nil, errors.ErrNoCompatibleCodec
	}

	sink, err := gst.NewElementWithName("multifilesink", fmt.Sprintf("multifilesink_%s", c.Id))
	if err != nil {
		return nil, err
	}
	err = sink.SetProperty("post-messages", true)
	if err != nil {
		return nil, err
	}

	// File will be renamed if the TS prefix is configured
	location := fmt.Sprintf("%s_%%05d%s", path.Join(c.LocalDir, c.ImagePrefix), types.FileExtensionForOutputType[c.OutputType])

	err = sink.SetProperty("location", location)
	if err != nil {
		return nil, err
	}
	if err := b.AddElements(sink); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return b, nil
}
