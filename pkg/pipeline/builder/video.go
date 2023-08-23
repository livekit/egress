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
	"sync"
	"time"
	"unsafe"

	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

const videoTestSrc = "video_test_src"

type videoBin struct {
	lastPTS     atomic.Duration
	nextPTS     atomic.Duration
	selectedPad string
	nextPad     *gst.Pad

	mu       sync.Mutex
	pads     map[string]*gst.Pad
	selector *gst.Element
}

func BuildVideoBin(pipeline *gstreamer.Pipeline, p *config.PipelineConfig) (*gstreamer.Bin, error) {
	b := pipeline.NewBin("video")

	switch p.SourceType {
	case types.SourceTypeSDK:
		if err := buildSDKVideoInput(b, p); err != nil {
			return nil, err
		}

	case types.SourceTypeWeb:
		if err := buildWebVideoInput(b, p); err != nil {
			return nil, err
		}
	}

	if len(p.Outputs) > 1 {
		tee, err := gst.NewElementWithName("tee", "video_tee")
		if err != nil {
			return nil, err
		}

		if err = b.AddElement(tee); err != nil {
			return nil, err
		}
	}

	return b, nil
}

func buildWebVideoInput(b *gstreamer.Bin, p *config.PipelineConfig) error {
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

	videoQueue, err := gstreamer.BuildQueue("video_input_queue", p.Latency, true)
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

	if err = b.AddElements(xImageSrc, videoQueue, videoConvert, caps); err != nil {
		return err
	}

	if p.VideoTranscoding {
		if err = addVideoEncoder(b, p); err != nil {
			return err
		}
	}
	return nil
}

func buildSDKVideoInput(b *gstreamer.Bin, p *config.PipelineConfig) error {
	v := &videoBin{}

	if p.VideoTrack != nil {
		if err := v.buildVideoAppSrcBin(b, p); err != nil {
			return err
		}
	}

	if p.VideoTranscoding {
		if err := v.buildVideoTestSrcBin(b, p); err != nil {
			return err
		}
		if err := v.addVideoSelector(b); err != nil {
			return err
		}

		b.SetGetSrcPad(v.getSrcPad)
		b.Callbacks.AddOnTrackMuted(v.onTrackMuted)
		b.Callbacks.AddOnTrackUnmuted(v.onTrackUnmuted)

		if err := addVideoEncoder(b, p); err != nil {
			return err
		}
	}

	return nil
}

func (v *videoBin) buildVideoAppSrcBin(videoBin *gstreamer.Bin, p *config.PipelineConfig) error {
	track := p.VideoTrack

	b := videoBin.NewBin(track.TrackID)
	b.SetEOSFunc(track.EOSFunc)
	if err := videoBin.AddSourceBin(b); err != nil {
		return err
	}

	track.AppSrc.Element.SetArg("format", "time")
	if err := track.AppSrc.Element.SetProperty("is-live", true); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if err := b.AddElement(track.AppSrc.Element); err != nil {
		return err
	}

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

		caps, err := gst.NewElement("capsfilter")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = caps.SetProperty("caps", gst.NewCapsFromString(
			"video/x-h264,stream-format=byte-stream",
		)); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		if err = b.AddElements(rtpH264Depay, caps); err != nil {
			return err
		}

		if p.VideoTranscoding {
			avDecH264, err := gst.NewElement("avdec_h264")
			if err != nil {
				return errors.ErrGstPipelineError(err)
			}

			if err = b.AddElement(avDecH264); err != nil {
				return err
			}
		} else {
			h264Parse, err := gst.NewElement("h264parse")
			if err != nil {
				return errors.ErrGstPipelineError(err)
			}

			if err = b.AddElement(h264Parse); err != nil {
				return err
			}

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
		if err = b.AddElement(rtpVP8Depay); err != nil {
			return err
		}

		if p.VideoTranscoding {
			vp8Dec, err := gst.NewElement("vp8dec")
			if err != nil {
				return errors.ErrGstPipelineError(err)
			}
			if err = b.AddElement(vp8Dec); err != nil {
				return err
			}
		} else {
			return nil
		}

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
		if err = b.AddElement(rtpVP9Depay); err != nil {
			return err
		}

		if p.VideoTranscoding {
			vp9Dec, err := gst.NewElement("vp9dec")
			if err != nil {
				return errors.ErrGstPipelineError(err)
			}
			if err = b.AddElement(vp9Dec); err != nil {
				return err
			}
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

			if err = b.AddElements(vp9Parse, vp9Caps); err != nil {
				return err
			}
			return nil
		}

	default:
		return errors.ErrNotSupported(string(track.MimeType))
	}

	videoQueue, err := gstreamer.BuildQueue("video_input_queue", p.Latency, true)
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

	if err = b.AddElements(videoQueue, videoConvert, videoScale, videoRate, caps); err != nil {
		return err
	}

	return nil
}

func (v *videoBin) buildVideoTestSrcBin(videoBin *gstreamer.Bin, p *config.PipelineConfig) error {
	b := videoBin.NewBin(videoTestSrc)
	if err := videoBin.AddSourceBin(b); err != nil {
		return err
	}

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

	if err = b.AddElements(videoTestSrc, caps); err != nil {
		return err
	}

	return nil
}

func (v *videoBin) addVideoSelector(b *gstreamer.Bin) error {
	inputSelector, err := gst.NewElement("input-selector")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = b.AddElement(inputSelector); err != nil {
		return err
	}
	return nil
}

func addVideoEncoder(b *gstreamer.Bin, p *config.PipelineConfig) error {
	// Put a queue in front of the encoder for pipelining with the stage before
	videoQueue, err := gstreamer.BuildQueue("video_encoder_queue", p.Latency, false)
	if err != nil {
		return err
	}
	if err = b.AddElement(videoQueue); err != nil {
		return err
	}

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
			// avoid key frames other than at segments boundaries as splitmuxsink can become inconsistent otherwise
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

		if err = b.AddElements(x264Enc, caps); err != nil {
			return err
		}

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
		if err = b.AddElement(vp9Enc); err != nil {
			return err
		}

		fallthrough

	default:
		return errors.ErrNotSupported(fmt.Sprintf("%s encoding", p.VideoOutCodec))
	}
}

func (v *videoBin) getSrcPad(name string) *gst.Pad {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.pads[name]
}

func (v *videoBin) createSrcPad(trackID string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	pad := v.selector.GetRequestPad("sink_%u")
	pad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buffer := info.GetBuffer()

		for v.nextPTS.Load() != 0 {
			time.Sleep(time.Millisecond * 100)
		}

		v.lastPTS.Store(buffer.PresentationTimestamp())

		logger.Debugw("pushing src buffer", "pts", buffer.PresentationTimestamp())
		return gst.PadProbeOK
	})
	v.pads[trackID] = pad
}

func (v *videoBin) createTestSrcPad() {
	v.mu.Lock()
	defer v.mu.Unlock()

	pad := v.selector.GetRequestPad("sink_%u")
	pad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buffer := info.GetBuffer()

		if nextPTS := v.nextPTS.Load(); nextPTS != 0 && buffer.PresentationTimestamp() >= nextPTS {
			if err := v.setSelectorPad(v.nextPad); err != nil {
				logger.Errorw("failed to unmute", err)
				return gst.PadProbeDrop
			}
			v.nextPTS.Store(0)
		} else if buffer.PresentationTimestamp() < v.lastPTS.Load() {
			return gst.PadProbeDrop
		}

		logger.Debugw("pushing test src buffer", "pts", buffer.PresentationTimestamp())
		return gst.PadProbeOK
	})
	v.pads[videoTestSrc] = pad
}

func (v *videoBin) onTrackMuted(trackID string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.selectedPad != trackID {
		return
	}

	pad := v.pads[videoTestSrc]
	if err := v.setSelectorPad(pad); err != nil {
		logger.Errorw("failed to set selector pad", err)
	}
}

func (v *videoBin) onTrackUnmuted(trackID string, pts time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()

	pad := v.pads[trackID]
	if pad == nil {
		return
	}

	v.nextPTS.Store(pts)
	v.nextPad = pad
}

func (v *videoBin) setSelectorPad(pad *gst.Pad) error {
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
