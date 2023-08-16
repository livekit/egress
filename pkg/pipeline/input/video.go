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

package input

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

type videoInput struct {
	mu      sync.Mutex
	trackID string
	lastPTS atomic.Duration
	nextPTS atomic.Duration

	src        []*gst.Element
	srcPad     *gst.Pad
	testSrc    []*gst.Element
	testSrcPad *gst.Pad
	selector   *gst.Element
	encoder    []*gst.Element
}

func (v *videoInput) buildWebInput(p *config.PipelineConfig) error {
	xImageSrc, err := gst.NewElement("ximagesrc")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = xImageSrc.SetProperty("display-name", p.Display); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = xImageSrc.SetProperty("use-damage", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = xImageSrc.SetProperty("show-pointer", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	videoQueue, err := builder.BuildQueue("video_input_queue", p.Latency, true)
	if err != nil {
		return err
	}

	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = caps.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
		"video/x-raw,framerate=%d/1",
		p.Framerate,
	),
	)); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	v.src = []*gst.Element{xImageSrc, videoQueue, videoConvert, caps}
	return nil
}

func (v *videoInput) buildSDKInput(p *config.PipelineConfig) error {
	if p.VideoTrack != nil {
		if err := v.buildAppSource(p, p.VideoTrack); err != nil {
			return err
		}
	}

	if p.RequestType == types.RequestTypeParticipant {
		if err := v.buildTestSrc(p); err != nil {
			return err
		}

		return v.buildInputSelector()
	}

	return nil
}

func (v *videoInput) buildAppSource(p *config.PipelineConfig, track *config.TrackSource) error {
	v.trackID = track.TrackID

	track.AppSrc.Element.SetArg("format", "time")
	if err := track.AppSrc.Element.SetProperty("is-live", true); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	v.src = append(v.src, track.AppSrc.Element)
	switch track.MimeType {
	case types.MimeTypeH264:
		if err := track.AppSrc.Element.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=video,payload=%d,encoding-name=H264,clock-rate=%d",
			track.PayloadType, track.ClockRate,
		))); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		rtpH264Depay, err := gst.NewElement("rtph264depay")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		v.src = append(v.src, rtpH264Depay)

		caps, err := gst.NewElement("capsfilter")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = caps.SetProperty("caps", gst.NewCapsFromString(
			"video/x-h264,stream-format=byte-stream",
		)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		v.src = append(v.src, caps)

		if p.VideoTranscoding {
			avDecH264, err := gst.NewElement("avdec_h264")
			if err != nil {
				return errors.ErrGstPipelineError(err)
			}

			v.src = append(v.src, avDecH264)
		} else {
			h264Parse, err := gst.NewElement("h264parse")
			if err != nil {
				return errors.ErrGstPipelineError(err)
			}

			v.src = append(v.src, h264Parse)
			return nil
		}

	case types.MimeTypeVP8:
		if err := track.AppSrc.Element.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=video,payload=%d,encoding-name=VP8,clock-rate=%d",
			track.PayloadType, track.ClockRate,
		))); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		rtpVP8Depay, err := gst.NewElement("rtpvp8depay")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		v.src = append(v.src, rtpVP8Depay)

		if !p.VideoTranscoding {
			return nil
		}

		vp8Dec, err := gst.NewElement("vp8dec")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		v.src = append(v.src, vp8Dec)

	case types.MimeTypeVP9:
		if err := track.AppSrc.Element.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=video,payload=%d,encoding-name=VP9,clock-rate=%d",
			track.PayloadType, track.ClockRate,
		))); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		rtpVP9Depay, err := gst.NewElement("rtpvp9depay")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		v.src = append(v.src, rtpVP9Depay)

		if p.VideoTranscoding {
			vp9Dec, err := gst.NewElement("vp9dec")
			if err != nil {
				return errors.ErrGstPipelineError(err)
			}

			v.src = append(v.src, vp9Dec)
		} else {
			vp9Parse, err := gst.NewElement("vp9parse")
			if err != nil {
				return errors.ErrGstPipelineError(err)
			}

			vp9Caps, err := gst.NewElement("capsfilter")
			if err != nil {
				return errors.ErrGstPipelineError(err)
			}
			if err = vp9Caps.SetProperty("caps", gst.NewCapsFromString(
				"video/x-vp9,width=[16,2147483647],height=[16,2147483647]",
			)); err != nil {
				return errors.ErrGstPipelineError(err)
			}

			v.src = append(v.src, vp9Parse, vp9Caps)
			return nil
		}

	default:
		return errors.ErrNotSupported(string(track.MimeType))
	}

	videoQueue, err := builder.BuildQueue("video_input_queue", p.Latency, true)
	if err != nil {
		return err
	}

	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	videoScale, err := gst.NewElement("videoscale")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	videoRate, err := gst.NewElement("videorate")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = caps.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
		"video/x-raw,framerate=%d/1,format=I420,width=%d,height=%d,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1",
		p.Framerate, p.Width, p.Height,
	))); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	v.src = append(v.src, videoQueue, videoConvert, videoScale, videoRate, caps)
	return nil
}

func (v *videoInput) buildTestSrc(p *config.PipelineConfig) error {
	videoTestSrc, err := gst.NewElement("videotestsrc")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = videoTestSrc.SetProperty("is-live", true); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	videoTestSrc.SetArg("pattern", "black")

	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = caps.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
		"video/x-raw,framerate=%d/1,format=I420,width=%d,height=%d,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1",
		p.Framerate, p.Width, p.Height,
	))); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	v.testSrc = []*gst.Element{videoTestSrc, caps}
	return nil
}

func (v *videoInput) buildInputSelector() error {
	inputSelector, err := gst.NewElement("input-selector")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	v.selector = inputSelector
	return nil
}

func (v *videoInput) buildEncoder(p *config.PipelineConfig) error {
	// Put a queue in front of the encoder for pipelining with the stage before
	videoQueue, err := builder.BuildQueue("video_encoder_queue", p.Latency, false)
	if err != nil {
		return err
	}
	v.encoder = append(v.encoder, videoQueue)

	switch p.VideoOutCodec {
	// we only encode h264, the rest are too slow
	case types.MimeTypeH264:
		x264Enc, err := gst.NewElement("x264enc")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = x264Enc.SetProperty("bitrate", uint(p.VideoBitrate)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		x264Enc.SetArg("speed-preset", "veryfast")
		if p.KeyFrameInterval != 0 {
			if err = x264Enc.SetProperty("key-int-max", uint(p.KeyFrameInterval*float64(p.Framerate))); err != nil {
				return errors.ErrGstPipelineError(err)
			}
		}
		if p.GetSegmentConfig() != nil {
			// Avoid key frames other than at segments boundaries as splitmuxsink can become inconsistent otherwise
			if err = x264Enc.SetProperty("option-string", "scenecut=0"); err != nil {
				return errors.ErrGstPipelineError(err)
			}
		}

		caps, err := gst.NewElement("capsfilter")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = caps.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"video/x-h264,profile=%s",
			p.VideoProfile,
		))); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		v.encoder = append(v.encoder, x264Enc, caps)
		return nil

	case types.MimeTypeVP9:
		vp9Enc, err := gst.NewElement("vp9enc")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = vp9Enc.SetProperty("deadline", int64(1)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = vp9Enc.SetProperty("row-mt", true); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = vp9Enc.SetProperty("tile-columns", 3); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = vp9Enc.SetProperty("tile-rows", 1); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = vp9Enc.SetProperty("frame-parallel", true); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = vp9Enc.SetProperty("max-quantizer", 52); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = vp9Enc.SetProperty("min-quantizer", 2); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		v.encoder = append(v.encoder, vp9Enc)
		fallthrough

	default:
		return errors.ErrNotSupported(fmt.Sprintf("%s encoding", p.VideoOutCodec))
	}
}

func (v *videoInput) link() (*gst.GhostPad, error) {
	if v.src != nil {
		// link src elements
		if err := gst.ElementLinkMany(v.src...); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}

	if v.testSrc != nil {
		// link test src elements
		if err := gst.ElementLinkMany(v.testSrc...); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}

	if v.selector != nil {
		if v.src != nil {
			v.createSrcPad()
			if err := builder.LinkPads(
				"video src", builder.GetSrcPad(v.src),
				"video selector", v.srcPad,
			); err != nil {
				return nil, err
			}
		}

		if v.testSrc != nil {
			v.createTestSrcPad()
			if err := builder.LinkPads(
				"video test src", builder.GetSrcPad(v.testSrc),
				"video selector", v.testSrcPad,
			); err != nil {
				return nil, err
			}
		}

		if v.src != nil {
			if err := v.setSelectorPad(v.srcPad); err != nil {
				return nil, err
			}
		} else {
			if err := v.setSelectorPad(v.testSrcPad); err != nil {
				return nil, err
			}
		}
	}

	var ghostPad *gst.Pad
	if v.encoder != nil {
		if err := gst.ElementLinkMany(v.encoder...); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		if v.selector != nil {
			if err := builder.LinkPads(
				"video selector", v.selector.GetStaticPad("src"),
				"video encoder", v.encoder[0].GetStaticPad("sink"),
			); err != nil {
				return nil, err
			}
		} else {
			if err := builder.LinkPads(
				"video src", builder.GetSrcPad(v.src),
				"video encoder", v.encoder[0].GetStaticPad("sink"),
			); err != nil {
				return nil, err
			}
		}

		ghostPad = builder.GetSrcPad(v.encoder)
	} else if v.selector != nil {
		ghostPad = v.selector.GetStaticPad("src")
	} else {
		ghostPad = builder.GetSrcPad(v.src)
	}

	return gst.NewGhostPad("video_src", ghostPad), nil
}

func (v *videoInput) linkAppSrc() error {
	v.createSrcPad()

	if err := gst.ElementLinkMany(v.src...); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if err := builder.LinkPads(
		"video src", builder.GetSrcPad(v.src),
		"video selector", v.srcPad,
	); err != nil {
		return err
	}

	return v.setSelectorPad(v.srcPad)
}

func (v *videoInput) removeAppSrc(bin *gst.Bin) error {
	// swap selector
	if err := v.setSelectorPad(v.testSrcPad); err != nil {
		return err
	}

	// change state
	for _, e := range v.src {
		if err := e.BlockSetState(gst.StateNull); err != nil {
			logger.Errorw("failed to stop audio src", err)
		}
	}

	// unlink
	builder.GetSrcPad(v.src).Unlink(v.srcPad)

	// remove elements
	if err := bin.RemoveMany(v.src...); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	// release elements and pads
	v.selector.ReleaseRequestPad(v.srcPad)
	v.src = nil
	v.srcPad = nil
	v.trackID = ""

	return nil
}

func (v *videoInput) createSrcPad() {
	v.srcPad = v.selector.GetRequestPad("sink_%u")
	v.srcPad.SetChainFunction(func(self *gst.Pad, parent *gst.Object, buffer *gst.Buffer) gst.FlowReturn {
		// Buffer gets automatically unreferenced by go-gst.
		// Without referencing it here, it will sometimes be garbage collected before being written
		buffer.Ref()

		logger.Debugw("srcpad")
		v.lastPTS.Store(buffer.PresentationTimestamp())

		internal, _ := self.GetInternalLinks()
		if len(internal) != 1 {
			return gst.FlowNotLinked
		}
		return internal[0].Push(buffer)
	})
}

func (v *videoInput) createTestSrcPad() {
	v.testSrcPad = v.selector.GetRequestPad("sink_%u")
	v.testSrcPad.SetChainFunction(func(self *gst.Pad, parent *gst.Object, buffer *gst.Buffer) gst.FlowReturn {
		// Buffer gets automatically unreferenced by go-gst.
		// Without referencing it here, it will sometimes be garbage collected before being written
		buffer.Ref()

		logger.Debugw("testsrcpad")
		if buffer.PresentationTimestamp() > v.lastPTS.Load() {
			return gst.FlowOK
		}

		internal, _ := self.GetInternalLinks()
		if len(internal) != 1 {
			return gst.FlowNotLinked
		}
		return internal[0].Push(buffer)
	})
}

func (v *videoInput) setSelectorPad(pad *gst.Pad) error {
	// TODO: go-gst should accept objects directly and handle conversion to C
	pt, err := v.selector.GetPropertyType("active-pad")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	val, err := glib.ValueInit(pt)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	val.SetInstance(uintptr(unsafe.Pointer(pad.Instance())))

	if err = v.selector.SetPropertyValue("active-pad", val); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	return nil
}
