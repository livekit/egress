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
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
)

func BuildFileBin(pipeline *gstreamer.Pipeline, p *config.PipelineConfig) (*gstreamer.Bin, error) {
	b := pipeline.NewBin("file")
	o := p.GetFileConfig()

	var mux *gst.Element
	var err error
	switch o.OutputType {
	case types.OutputTypeOGG:
		mux, err = gst.NewElement("oggmux")
	case types.OutputTypeIVF:
		mux, err = gst.NewElement("avmux_ivf")
	case types.OutputTypeMP4:
		mux, err = gst.NewElement("mp4mux")
	case types.OutputTypeWebM:
		mux, err = gst.NewElement("webmmux")
	default:
		err = errors.ErrInvalidInput("output type")
	}
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	sink, err := gst.NewElement("filesink")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("location", o.LocalFilepath); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = sink.SetProperty("sync", false); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = b.AddElements(mux, sink); err != nil {
		return nil, err
	}

	b.SetGetSrcPad(func(name string) *gst.Pad {
		var padName = name + "_%u"

		return mux.GetRequestPad(padName)
	})

	return b, nil
}
