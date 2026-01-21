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
	"strings"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/linkdata/deadlock"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	videoTestSrcName = "video_test_src"
)

type VideoBin struct {
	bin  *gstreamer.Bin
	conf *config.PipelineConfig

	mu          deadlock.Mutex
	nextID      int
	selectedPad string
	lastPTS     uint64
	pads        map[string]*gst.Pad
	names       map[string]string
	selector    *gst.Element
	rawVideoTee *gst.Element
}

// buildLeakyVideoQueue creates a leaky queue and attaches a monitor to track dropped buffers
func (b *VideoBin) buildLeakyVideoQueue(name string) (*gst.Element, error) {
	queue, err := gstreamer.BuildQueue(name, b.conf.Latency.PipelineLatency, true)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	NewLeakyQueueMonitor(name, queue)

	return queue, nil
}

func BuildVideoBin(pipeline *gstreamer.Pipeline, p *config.PipelineConfig) error {
	b := &VideoBin{
		bin:  pipeline.NewBin("video"),
		conf: p,
	}

	switch p.SourceType {
	case types.SourceTypeWeb:
		if err := b.buildWebInput(); err != nil {
			return err
		}

	case types.SourceTypeSDK:
		if err := b.buildSDKInput(); err != nil {
			return err
		}

		pipeline.AddOnTrackAdded(b.onTrackAdded)
		pipeline.AddOnTrackRemoved(b.onTrackRemoved)
		pipeline.AddOnTrackMuted(b.onTrackMuted)
		pipeline.AddOnTrackUnmuted(b.onTrackUnmuted)
	}

	var getPad func() *gst.Pad
	if len(p.GetEncodedOutputs()) > 1 {
		tee, err := gst.NewElementWithName("tee", "video_tee")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		if err = b.bin.AddElement(tee); err != nil {
			return err
		}

		getPad = func() *gst.Pad {
			return tee.GetRequestPad("src_%u")
		}
	} else if len(p.GetEncodedOutputs()) > 0 {
		queue, err := b.buildLeakyVideoQueue("video_queue")
		if err != nil {
			return err
		}
		if err = b.bin.AddElement(queue); err != nil {
			return err
		}

		getPad = func() *gst.Pad {
			return queue.GetStaticPad("src")
		}
	}

	b.bin.SetGetSinkPad(func(name string) *gst.Pad {
		if strings.HasPrefix(name, "image") {
			return b.rawVideoTee.GetRequestPad("src_%u")
		} else if getPad != nil {
			return getPad()
		}

		return nil
	})

	return pipeline.AddSourceBin(b.bin)
}

func (b *VideoBin) onTrackAdded(ts *config.TrackSource) {
	if b.bin.GetState() > gstreamer.StateRunning {
		return
	}

	if ts.TrackKind == lksdk.TrackKindVideo {
		logger.Debugw("adding video app src bin", "trackID", ts.TrackID)
		if err := b.addAppSrcBin(ts); err != nil {
			logger.Errorw("failed to add video app src bin", err, "trackID", ts.TrackID)
			b.bin.OnError(err)
		}
	}
}

func (b *VideoBin) onTrackRemoved(trackID string) {
	if b.bin.GetState() > gstreamer.StateRunning {
		return
	}

	b.mu.Lock()
	name, ok := b.names[trackID]
	if !ok {
		b.mu.Unlock()
		return
	}
	delete(b.names, trackID)
	delete(b.pads, name)

	if b.selectedPad == name {
		if err := b.setSelectorPadLocked(videoTestSrcName); err != nil {
			b.mu.Unlock()
			b.bin.OnError(err)
			return
		}
	}
	b.mu.Unlock()

	if err := b.bin.RemoveSourceBin(name); err != nil {
		b.bin.OnError(err)
	}
}

func (b *VideoBin) onTrackMuted(trackID string) {
	if b.bin.GetState() > gstreamer.StateRunning {
		return
	}

	b.mu.Lock()
	if name, ok := b.names[trackID]; ok && b.selectedPad == name {
		if err := b.setSelectorPadLocked(videoTestSrcName); err != nil {
			b.mu.Unlock()
			b.bin.OnError(err)
			return
		}
	}
	b.mu.Unlock()
}

func (b *VideoBin) onTrackUnmuted(trackID string) {
	if b.bin.GetState() > gstreamer.StateRunning {
		return
	}

	b.mu.Lock()
	if name, ok := b.names[trackID]; ok {
		if err := b.setSelectorPadLocked(name); err != nil {
			b.mu.Unlock()
			b.bin.OnError(err)
			return
		}
	}
	b.mu.Unlock()
}

func (b *VideoBin) buildWebInput() error {
	xImageSrc, err := gst.NewElement("ximagesrc")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = xImageSrc.SetProperty("display-name", b.conf.Display); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = xImageSrc.SetProperty("use-damage", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = xImageSrc.SetProperty("show-pointer", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	videoQueue, err := b.buildLeakyVideoQueue("video_input_queue")
	if err != nil {
		return err
	}

	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	videoRate, err := gst.NewElement("videorate")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = videoRate.SetProperty("skip-to-first", true); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = caps.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
		"video/x-raw,framerate=%d/1",
		b.conf.Framerate,
	),
	)); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if err = b.bin.AddElements(xImageSrc, videoQueue, videoConvert, videoRate, caps); err != nil {
		return err
	}

	return b.addDecodedVideoSink()
}

func (b *VideoBin) buildSDKInput() error {
	b.pads = make(map[string]*gst.Pad)
	b.names = make(map[string]string)

	// add selector first so pads can be created
	if b.conf.VideoDecoding {
		if err := b.addSelector(); err != nil {
			return err
		}
	}

	if b.conf.VideoTrack != nil {
		if err := b.addAppSrcBin(b.conf.VideoTrack); err != nil {
			return err
		}
	}

	if b.conf.VideoDecoding {
		b.bin.SetGetSrcPad(b.getSrcPad)

		if err := b.addVideoTestSrcBin(); err != nil {
			return err
		}
		if b.conf.VideoTrack == nil {
			if err := b.setSelectorPad(videoTestSrcName); err != nil {
				return err
			}
		}
		if err := b.addDecodedVideoSink(); err != nil {
			return err
		}
	}

	return nil
}

func (b *VideoBin) addAppSrcBin(ts *config.TrackSource) error {
	name := fmt.Sprintf("%s_%d", ts.TrackID, b.nextID)
	b.nextID++

	appSrcBin, err := b.buildAppSrcBin(ts, name)
	if err != nil {
		return err
	}

	if b.conf.VideoDecoding {
		b.createSrcPad(ts.TrackID, name)
	}

	if err = b.bin.AddSourceBin(appSrcBin); err != nil {
		return err
	}

	if b.conf.VideoDecoding {
		return b.setSelectorPad(name)
	}

	return nil
}

func (b *VideoBin) buildAppSrcBin(ts *config.TrackSource, name string) (*gstreamer.Bin, error) {
	appSrcBin := b.bin.NewBin(name)
	appSrcBin.SetEOSFunc(func() bool {
		return false
	})
	ts.AppSrc.Element.SetArg("format", "time")
	if err := ts.AppSrc.Element.SetProperty("is-live", true); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err := appSrcBin.AddElement(ts.AppSrc.Element); err != nil {
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

		if err = appSrcBin.AddElements(rtpH264Depay, caps); err != nil {
			return nil, err
		}

		if !b.conf.VideoDecoding {
			h264ParseFixer, err := newPTSFixer("h264parse", fmt.Sprintf("track:%s", ts.TrackID))
			if err != nil {
				return nil, err
			}

			if err = appSrcBin.AddElement(h264ParseFixer.Element); err != nil {
				return nil, err
			}

			return appSrcBin, nil
		}

		avDecH264, err := gst.NewElement("avdec_h264")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		if err = appSrcBin.AddElement(avDecH264); err != nil {
			return nil, err
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
		if err = appSrcBin.AddElement(rtpVP8Depay); err != nil {
			return nil, err
		}

		if !b.conf.VideoDecoding {
			return appSrcBin, nil
		}
		vp8Dec, err := gst.NewElement("vp8dec")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = appSrcBin.AddElement(vp8Dec); err != nil {
			return nil, err
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
		if err = appSrcBin.AddElement(rtpVP9Depay); err != nil {
			return nil, err
		}

		if !b.conf.VideoDecoding {
			vp9ParseFixer, err := newPTSFixer("vp9parse", fmt.Sprintf("track:%s", ts.TrackID))
			if err != nil {
				return nil, err
			}
			vp9Parse := vp9ParseFixer.Element

			vp9Caps, err := gst.NewElement("capsfilter")
			if err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}
			if err = vp9Caps.SetProperty("caps", gst.NewCapsFromString(
				"video/x-vp9,width=[16,2147483647],height=[16,2147483647]",
			)); err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}

			if err = appSrcBin.AddElements(vp9Parse, vp9Caps); err != nil {
				return nil, err
			}
			return appSrcBin, nil
		}

		vp9Dec, err := gst.NewElement("vp9dec")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = appSrcBin.AddElement(vp9Dec); err != nil {
			return nil, err
		}

	default:
		return nil, errors.ErrNotSupported(string(ts.MimeType))
	}

	if err := b.addVideoConverter(appSrcBin); err != nil {
		return nil, err
	}

	return appSrcBin, nil
}

func (b *VideoBin) addVideoTestSrcBin() error {
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

	queue, err := gstreamer.BuildQueue("video_test_src_queue", b.conf.Latency.PipelineLatency, false)
	if err != nil {
		return err
	}
	if err = queue.SetProperty("min-threshold-time", uint64(2e9)); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	caps, err := b.newVideoCapsFilter(true)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if err = testSrcBin.AddElements(videoTestSrc, queue, caps); err != nil {
		return err
	}

	b.createTestSrcPad()
	return nil
}

func (b *VideoBin) addSelector() error {
	inputSelector, err := gst.NewElement("input-selector")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	videoRate, err := gst.NewElement("videorate")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = videoRate.SetProperty("skip-to-first", true); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	caps, err := b.newVideoCapsFilter(true)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if err = b.bin.AddElements(inputSelector, videoRate, caps); err != nil {
		return err
	}

	b.selector = inputSelector
	return nil
}

func (b *VideoBin) addEncoder() error {
	videoQueue, err := gstreamer.BuildQueue("video_encoder_queue", b.conf.Latency.PipelineLatency, false)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = b.bin.AddElement(videoQueue); err != nil {
		return err
	}

	switch b.conf.VideoOutCodec {
	// we only encode h264, the rest are too slow
	case types.MimeTypeH264:
		x264Enc, err := gst.NewElement("x264enc")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		x264Enc.SetArg("speed-preset", "veryfast")

		var options []string
		disabledSceneCut := false
		// Streaming outputs always set KeyFrameInterval, so this effectively disables scenecut for RTMP/SRT.
		if b.conf.KeyFrameInterval != 0 {
			keyframeInterval := uint(b.conf.KeyFrameInterval * float64(b.conf.Framerate))
			if err = x264Enc.SetProperty("key-int-max", keyframeInterval); err != nil {
				return errors.ErrGstPipelineError(err)
			}
			options = append(options, "scenecut=0")
			disabledSceneCut = true
		}

		bufCapacity := uint(2000) // 2s
		if b.conf.GetSegmentConfig() != nil {
			// avoid key frames other than at segments boundaries as splitmuxsink can become inconsistent otherwise
			if !disabledSceneCut {
				options = append(options, "scenecut=0")
				disabledSceneCut = true
			}
			bufCapacity = uint(time.Duration(b.conf.GetSegmentConfig().SegmentDuration) * (time.Second / time.Millisecond))
		}
		if bufCapacity > 10000 {
			// Max value allowed by gstreamer
			bufCapacity = 10000
		}
		if err = x264Enc.SetProperty("vbv-buf-capacity", bufCapacity); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		if err = x264Enc.SetProperty("bitrate", uint(b.conf.VideoBitrate)); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		if sc := b.conf.GetStreamConfig(); sc != nil && sc.OutputType == types.OutputTypeRTMP {
			options = append(options, "nal-hrd=cbr")
		}
		if len(options) > 0 {
			optionString := strings.Join(options, ":")
			if err = x264Enc.SetProperty("option-string", optionString); err != nil {
				return errors.ErrGstPipelineError(err)
			}
		}

		caps, err := gst.NewElement("capsfilter")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = caps.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"video/x-h264,profile=%s,multiview-mode=mono,multiview-flags=(GstVideoMultiviewFlagsSet)0:ffffffff:/right-view-first/left-flipped/left-flopped/right-flipped/right-flopped/half-aspect/mixed-mono",
			b.conf.VideoProfile,
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
		return errors.ErrNotSupported(fmt.Sprintf("%s encoding", b.conf.VideoOutCodec))
	}
}

func (b *VideoBin) addDecodedVideoSink() error {
	var err error
	b.rawVideoTee, err = gst.NewElement("tee")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = b.bin.AddElement(b.rawVideoTee); err != nil {
		return err
	}

	if b.conf.VideoEncoding {
		err = b.addEncoder()
		if err != nil {
			return err
		}
	}

	return nil
}

func (b *VideoBin) addVideoConverter(bin *gstreamer.Bin) error {
	videoQueue, err := b.buildLeakyVideoQueue("video_input_queue")
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

	elements := []*gst.Element{videoQueue, videoConvert, videoScale}

	if !b.conf.VideoDecoding {
		videoRate, err := gst.NewElement("videorate")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = videoRate.SetProperty("skip-to-first", true); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		elements = append(elements, videoRate)
	}

	caps, err := b.newVideoCapsFilter(!b.conf.VideoDecoding)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	elements = append(elements, caps)

	return bin.AddElements(elements...)
}

func (b *VideoBin) newVideoCapsFilter(includeFramerate bool) (*gst.Element, error) {
	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if includeFramerate {
		err = caps.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"video/x-raw,framerate=%d/1,format=I420,width=%d,height=%d,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1",
			b.conf.Framerate, b.conf.Width, b.conf.Height,
		)))
	} else {
		err = caps.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"video/x-raw,format=I420,width=%d,height=%d,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1",
			b.conf.Width, b.conf.Height,
		)))
	}
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	return caps, nil
}

func (b *VideoBin) getSrcPad(name string) *gst.Pad {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.pads[name]
}

func (b *VideoBin) createSrcPad(trackID, name string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.names[trackID] = name

	pad := b.selector.GetRequestPad("sink_%u")
	pad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		pts := uint64(info.GetBuffer().PresentationTimestamp())
		b.mu.Lock()
		if pts < b.lastPTS || (b.selectedPad != videoTestSrcName && b.selectedPad != name) {
			b.mu.Unlock()
			return gst.PadProbeDrop
		}
		b.lastPTS = pts
		b.mu.Unlock()
		return gst.PadProbeOK
	})

	b.pads[name] = pad
}

func (b *VideoBin) createTestSrcPad() {
	b.mu.Lock()
	defer b.mu.Unlock()

	pad := b.selector.GetRequestPad("sink_%u")
	pad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		pts := uint64(info.GetBuffer().PresentationTimestamp())
		b.mu.Lock()
		if pts < b.lastPTS || (b.selectedPad != videoTestSrcName) {
			b.mu.Unlock()
			return gst.PadProbeDrop
		}
		b.lastPTS = pts
		b.mu.Unlock()
		return gst.PadProbeOK
	})

	b.pads[videoTestSrcName] = pad
}

func (b *VideoBin) setSelectorPad(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.setSelectorPadLocked(name)
}

func (b *VideoBin) setSelectorPadLocked(name string) error {
	pad := b.pads[name]

	// drop until the next keyframe
	pad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buffer := info.GetBuffer()
		if buffer.HasFlags(gst.BufferFlagDeltaUnit) {
			return gst.PadProbeDrop
		}
		logger.Debugw("active pad changed", "name", name)
		return gst.PadProbeRemove
	})

	if err := b.selector.SetProperty("active-pad", pad); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	b.selectedPad = name
	return nil
}
