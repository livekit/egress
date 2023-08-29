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

const videoTestSrcName = "video_test_src"

type VideoBin struct {
	bin *gstreamer.Bin

	lastPTS     atomic.Duration
	nextPTS     atomic.Duration
	selectedPad string
	nextPad     string

	mu       sync.Mutex
	pads     map[string]*gst.Pad
	selector *gst.Element
}

func BuildVideoBin(pipeline *gstreamer.Pipeline, p *config.PipelineConfig) (*VideoBin, error) {
	b := &VideoBin{
		bin: pipeline.NewBin("video"),
	}

	switch p.SourceType {
	case types.SourceTypeWeb:
		if err := b.buildWebInput(p); err != nil {
			return nil, err
		}

	case types.SourceTypeSDK:
		if err := b.buildSDKInput(p); err != nil {
			return nil, err
		}
	}

	if len(p.Outputs) > 1 {
		tee, err := gst.NewElementWithName("tee", "video_tee")
		if err != nil {
			return nil, err
		}

		if err = b.bin.AddElement(tee); err != nil {
			return nil, err
		}
	}

	if err := pipeline.AddSourceBin(b.bin); err != nil {
		return nil, err
	}

	return b, nil
}

func (b *VideoBin) AddVideoAppSrcBin(p *config.PipelineConfig, ts *config.TrackSource) error {
	appSrcBin, err := buildVideoAppSrcBin(b.bin, p, ts)
	if err != nil {
		return err
	}

	if p.VideoTranscoding {
		b.createSrcPad(ts.TrackID)
	}

	if err = b.bin.AddSourceBin(appSrcBin); err != nil {
		return err
	}

	if p.VideoTranscoding {
		return b.setSelectorPad(ts.TrackID)
	}

	return nil
}

func (b *VideoBin) getSrcPad(name string) *gst.Pad {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.pads[name]
}

func (b *VideoBin) onTrackMuted(trackID string) {
	if b.selectedPad != trackID {
		return
	}

	if err := b.setSelectorPad(videoTestSrcName); err != nil {
		logger.Errorw("failed to set selector pad", err)
	}
}

func (b *VideoBin) onTrackUnmuted(trackID string, pts time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.pads[trackID] == nil {
		return
	}

	b.nextPTS.Store(pts)
	b.nextPad = trackID
}

func (b *VideoBin) createSrcPad(trackID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	pad := b.selector.GetRequestPad("sink_%u")
	pad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buffer := info.GetBuffer()
		for b.nextPTS.Load() != 0 {
			time.Sleep(time.Millisecond * 100)
		}
		if buffer.PresentationTimestamp() < b.lastPTS.Load() {
			return gst.PadProbeDrop
		}
		b.lastPTS.Store(buffer.PresentationTimestamp())
		return gst.PadProbeOK
	})
	b.pads[trackID] = pad
}

func (b *VideoBin) createTestSrcPad() {
	b.mu.Lock()
	defer b.mu.Unlock()

	pad := b.selector.GetRequestPad("sink_%u")
	pad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buffer := info.GetBuffer()
		if buffer.PresentationTimestamp() < b.lastPTS.Load() {
			return gst.PadProbeDrop
		}
		if nextPTS := b.nextPTS.Load(); nextPTS != 0 && buffer.PresentationTimestamp() >= nextPTS {
			if err := b.setSelectorPad(b.nextPad); err != nil {
				logger.Errorw("failed to unmute", err)
				return gst.PadProbeDrop
			}
			b.nextPad = ""
			b.nextPTS.Store(0)
		}
		b.lastPTS.Store(buffer.PresentationTimestamp())
		return gst.PadProbeOK
	})
	b.pads[videoTestSrcName] = pad
}

// TODO: go-gst should accept objects directly and handle conversion to C
func (b *VideoBin) setSelectorPad(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	pad := b.pads[name]

	pt, err := b.selector.GetPropertyType("active-pad")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	val, err := glib.ValueInit(pt)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	val.SetInstance(uintptr(unsafe.Pointer(pad.Instance())))

	if err = b.selector.SetPropertyValue("active-pad", val); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	b.selectedPad = name
	return nil
}

func (b *VideoBin) buildWebInput(p *config.PipelineConfig) error {
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

	if err = b.bin.AddElements(xImageSrc, videoQueue, videoConvert, caps); err != nil {
		return err
	}

	if p.VideoTranscoding {
		if err = b.addEncoder(p); err != nil {
			return err
		}
	}
	return nil
}

func (b *VideoBin) buildSDKInput(p *config.PipelineConfig) error {
	b.pads = make(map[string]*gst.Pad)

	// add selector first so pads can be created
	if p.VideoTranscoding {
		if err := b.addSelector(p); err != nil {
			return err
		}
	}

	if p.VideoTrack != nil {
		if err := b.AddVideoAppSrcBin(p, p.VideoTrack); err != nil {
			return err
		}
	}

	if p.VideoTranscoding {
		if err := b.addVideoTestSrcBin(p); err != nil {
			return err
		}

		if p.VideoTrack == nil {
			if err := b.setSelectorPad(videoTestSrcName); err != nil {
				return err
			}
		}

		b.bin.SetGetSrcPad(b.getSrcPad)
		b.bin.Callbacks.AddOnTrackMuted(b.onTrackMuted)
		b.bin.Callbacks.AddOnTrackUnmuted(b.onTrackUnmuted)

		if err := b.addEncoder(p); err != nil {
			return err
		}
	}

	return nil
}

func buildVideoAppSrcBin(videoBin *gstreamer.Bin, p *config.PipelineConfig, ts *config.TrackSource) (*gstreamer.Bin, error) {
	b := videoBin.NewBin(ts.TrackID)
	b.SetEOSFunc(ts.EOSFunc)

	ts.AppSrc.Element.SetArg("format", "time")
	if err := ts.AppSrc.Element.SetProperty("is-live", true); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err := b.AddElement(ts.AppSrc.Element); err != nil {
		return nil, err
	}

	switch ts.MimeType {
	case types.MimeTypeH264:
		if err := ts.AppSrc.Element.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=video,payload=%d,encoding-name=H264,clock-rate=%d",
			ts.PayloadType, ts.ClockRate,
		))); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		rtpH264Depay, err := gst.NewElement("rtph264depay")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		caps, err := gst.NewElement("capsfilter")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = caps.SetProperty("caps", gst.NewCapsFromString(
			"video/x-h264,stream-format=byte-stream",
		)); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		if err = b.AddElements(rtpH264Depay, caps); err != nil {
			return nil, err
		}

		if p.VideoTranscoding {
			avDecH264, err := gst.NewElement("avdec_h264")
			if err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}

			if err = b.AddElement(avDecH264); err != nil {
				return nil, err
			}
		} else {
			h264Parse, err := gst.NewElement("h264parse")
			if err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}

			if err = b.AddElement(h264Parse); err != nil {
				return nil, err
			}

			return b, nil
		}

	case types.MimeTypeVP8:
		if err := ts.AppSrc.Element.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=video,payload=%d,encoding-name=VP8,clock-rate=%d",
			ts.PayloadType, ts.ClockRate,
		))); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		rtpVP8Depay, err := gst.NewElement("rtpvp8depay")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = b.AddElement(rtpVP8Depay); err != nil {
			return nil, err
		}

		if p.VideoTranscoding {
			vp8Dec, err := gst.NewElement("vp8dec")
			if err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}
			if err = b.AddElement(vp8Dec); err != nil {
				return nil, err
			}
		} else {
			return b, nil
		}

	case types.MimeTypeVP9:
		if err := ts.AppSrc.Element.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=video,payload=%d,encoding-name=VP9,clock-rate=%d",
			ts.PayloadType, ts.ClockRate,
		))); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		rtpVP9Depay, err := gst.NewElement("rtpvp9depay")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = b.AddElement(rtpVP9Depay); err != nil {
			return nil, err
		}

		if p.VideoTranscoding {
			vp9Dec, err := gst.NewElement("vp9dec")
			if err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}
			if err = b.AddElement(vp9Dec); err != nil {
				return nil, err
			}
		} else {
			vp9Parse, err := gst.NewElement("vp9parse")
			if err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}

			vp9Caps, err := gst.NewElement("capsfilter")
			if err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}
			if err = vp9Caps.SetProperty("caps", gst.NewCapsFromString(
				"video/x-vp9,width=[16,2147483647],height=[16,2147483647]",
			)); err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}

			if err = b.AddElements(vp9Parse, vp9Caps); err != nil {
				return nil, err
			}

			return b, nil
		}

	default:
		return nil, errors.ErrNotSupported(string(ts.MimeType))
	}

	if err := addVideoConverter(b, p); err != nil {
		return nil, err
	}

	return b, nil
}

func (b *VideoBin) addVideoTestSrcBin(p *config.PipelineConfig) error {
	testSrcBin := b.bin.NewBin(videoTestSrcName)
	if err := b.bin.AddSourceBin(testSrcBin); err != nil {
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

	caps, err := newVideoCapsFilter(p, true)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if err = testSrcBin.AddElements(videoTestSrc, caps); err != nil {
		return err
	}

	b.createTestSrcPad()
	return nil
}

func (b *VideoBin) addSelector(p *config.PipelineConfig) error {
	inputSelector, err := gst.NewElement("input-selector")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	videoRate, err := gst.NewElement("videorate")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = videoRate.SetProperty("max-duplication-time", uint64(time.Second)); err != nil {
		return err
	}
	if err = videoRate.SetProperty("skip-to-first", true); err != nil {
		return err
	}

	caps, err := newVideoCapsFilter(p, true)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if err = b.bin.AddElements(inputSelector, videoRate, caps); err != nil {
		return err
	}

	b.selector = inputSelector
	return nil
}

func (b *VideoBin) addEncoder(p *config.PipelineConfig) error {
	videoQueue, err := gstreamer.BuildQueue("video_encoder_queue", p.Latency, false)
	if err != nil {
		return err
	}
	if err = b.bin.AddElement(videoQueue); err != nil {
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
		bufCapacity := uint(2000) // 2s
		if p.GetSegmentConfig() != nil {
			// avoid key frames other than at segments boundaries as splitmuxsink can become inconsistent otherwise
			if err = x264Enc.SetProperty("option-string", "scenecut=0"); err != nil {
				return errors.ErrGstPipelineError(err)
			}
			bufCapacity = uint(time.Duration(p.GetSegmentConfig().SegmentDuration) * (time.Second / time.Millisecond))
		}
		if err = x264Enc.SetProperty("vbv-buf-capacity", bufCapacity); err != nil {
			return err
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

		if err = b.bin.AddElements(x264Enc, caps); err != nil {
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
		if err = b.bin.AddElement(vp9Enc); err != nil {
			return err
		}

		fallthrough

	default:
		return errors.ErrNotSupported(fmt.Sprintf("%s encoding", p.VideoOutCodec))
	}
}

func addVideoConverter(b *gstreamer.Bin, p *config.PipelineConfig) error {
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
	if err = videoRate.SetProperty("max-duplication-time", uint64(time.Second)); err != nil {
		return err
	}
	if err = videoRate.SetProperty("skip-to-first", true); err != nil {
		return err
	}

	caps, err := newVideoCapsFilter(p, true)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	return b.AddElements(videoQueue, videoConvert, videoScale, videoRate, caps)
}

func newVideoCapsFilter(p *config.PipelineConfig, includeFramerate bool) (*gst.Element, error) {
	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if includeFramerate {
		err = caps.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"video/x-raw,framerate=%d/1,format=I420,width=%d,height=%d,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1",
			p.Framerate, p.Width, p.Height,
		)))
	} else {
		err = caps.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"video/x-raw,format=I420,width=%d,height=%d,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1",
			p.Width, p.Height,
		)))
	}
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	return caps, nil
}
