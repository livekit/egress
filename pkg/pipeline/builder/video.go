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
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	videoTestSrcName  = "video_test_src"
	videoTestSrcDelay = 2 * time.Second

	// what a layout-hidden track scales to, even because I420 requires it
	hiddenCellSize = 2
)

type VideoBin struct {
	bin  *gstreamer.Bin
	conf *config.PipelineConfig

	mu                 deadlock.Mutex
	nextID             int
	pads               map[string]*gst.Pad
	names              map[string]string
	muted              map[string]bool            // pad name -> muted; layout recalcs must not un-mute
	layoutAlpha        map[string]float64         // pad name -> alpha the layout wants; un-muting must not exceed it
	srcDims            map[string]videoDimensions // pad name -> incoming frame size, from the pad's caps
	cellDims           map[string]videoDimensions // pad name -> cell the layout gave it
	crops              map[string]*gst.Element    // pad name -> cover crop, applied before the scale
	inputCaps          map[string]*gst.Element    // pad name -> scale target, re-aimed on every layout change
	lastDimensions     map[string]videoDimensions
	selector           *gst.Element
	rawVideoTee        *gst.Element
	layout             *LayoutManager // nil when not compositing
	setVideoDimensions func(trackID string, width, height int)

	// input-selector state (only used when !Compositing)
	selectedPad string
	lastPTS     uint64

	probesMu deadlock.Mutex
	probes   map[string]*keyframeProbe
}

type videoDimensions struct {
	width  int
	height int
}

func BuildVideoBin(pipeline *gstreamer.Pipeline, p *config.PipelineConfig, setVideoDimensions func(string, int, int)) error {
	b := &VideoBin{
		bin:                pipeline.NewBin("video"),
		conf:               p,
		setVideoDimensions: setVideoDimensions,
		probes:             make(map[string]*keyframeProbe),
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
		pipeline.AddOnActiveSpeakersChanged(b.onActiveSpeakersChanged)
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
		queue, err := b.buildVideoQueue("video_queue")
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
			return
		}

		if b.layout != nil {
			source := TrackSourceCamera
			if ts.PublicationSource == livekit.TrackSource_SCREEN_SHARE {
				source = TrackSourceScreenShare
			}
			pads := b.layout.AddTrack(ts.TrackID, ts.ParticipantIdentity, source)
			if err := b.applyLayout(pads); err != nil {
				b.bin.OnError(err)
				return
			}
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
	departed := b.pads[name]
	delete(b.names, trackID)
	delete(b.pads, name)
	delete(b.muted, name)
	delete(b.layoutAlpha, name)
	delete(b.srcDims, name)
	delete(b.cellDims, name)
	delete(b.crops, name)
	delete(b.inputCaps, name)
	delete(b.lastDimensions, trackID)
	b.closeProbe(name)

	if !b.conf.Compositing && b.selectedPad == name {
		if err := b.setSelectorPadLocked(videoTestSrcName); err != nil {
			b.mu.Unlock()
			b.bin.OnError(err)
			return
		}
	}
	b.mu.Unlock()

	// the pad detaches asynchronously, so hide it before the survivors reflow
	// into the cell it still occupies
	if departed != nil && b.conf.Compositing {
		if err := departed.SetProperty("alpha", 0.0); err != nil {
			logger.Warnw("failed to hide departed pad", err, "name", name)
		}
	}

	if b.layout != nil {
		pads := b.layout.RemoveTrack(trackID)
		if err := b.applyLayout(pads); err != nil {
			b.bin.OnError(err)
			return
		}
	}

	if err := b.bin.RemoveSourceBin(name); err != nil {
		b.bin.OnError(err)
	}
}

func (b *VideoBin) buildVideoQueue(name string) (*gst.Element, error) {
	queue, err := gstreamer.BuildQueue(name, b.conf.Latency.PipelineLatency, b.conf.Live)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	return queue, nil
}

func (b *VideoBin) onTrackMuted(trackID string) {
	if b.bin.GetState() > gstreamer.StateRunning {
		return
	}

	b.mu.Lock()
	name, ok := b.names[trackID]
	b.mu.Unlock()
	if !ok {
		return
	}

	if err := b.setTrackVisible(name, false); err != nil {
		b.bin.OnError(err)
	}
}

func (b *VideoBin) onTrackUnmuted(trackID string) {
	if b.bin.GetState() > gstreamer.StateRunning {
		return
	}

	b.mu.Lock()
	name, ok := b.names[trackID]
	b.mu.Unlock()
	if !ok {
		return
	}

	if err := b.setTrackVisible(name, true); err != nil {
		b.bin.OnError(err)
	}
}

func (b *VideoBin) onActiveSpeakersChanged(speakers []lksdk.Participant) {
	if b.bin.GetState() > gstreamer.StateRunning {
		return
	}

	if b.layout == nil {
		return
	}

	speakerInfos := make([]SpeakerInfo, len(speakers))
	for i, s := range speakers {
		speakerInfos[i] = SpeakerInfo{
			Identity:   s.Identity(),
			AudioLevel: s.AudioLevel(),
			IsSpeaking: s.IsSpeaking(),
		}
	}

	pads := b.layout.UpdateSpeakers(speakerInfos)
	if pads != nil {
		if err := b.applyLayout(pads); err != nil {
			b.bin.OnError(err)
		}
	}
}

func (b *VideoBin) applyLayout(pads []PadLayout) error {
	b.mu.Lock()
	pending, err := b.applyLayoutLocked(pads)
	b.mu.Unlock()
	if err != nil {
		return err
	}

	// UpdateTrackDimensions signals the publisher, so it must not run under b.mu
	for _, d := range pending {
		b.setVideoDimensions(d.trackID, d.width, d.height)
	}
	return nil
}

type pendingDimensions struct {
	trackID string
	width   int
	height  int
}

// applyLayoutLocked writes the layout onto each pad and returns the dimension
// updates the caller must send after releasing b.mu.
func (b *VideoBin) applyLayoutLocked(pads []PadLayout) ([]pendingDimensions, error) {
	var pending []pendingDimensions

	for _, pl := range pads {
		name, ok := b.names[pl.TrackID]
		if !ok {
			continue
		}
		pad, ok := b.pads[name]
		if !ok {
			continue
		}

		// the layout calculators are unaware of mute state - it lives only as the
		// pad's alpha - so a muted track must stay hidden across a recalc
		b.layoutAlpha[name] = pl.Alpha
		alpha := pl.Alpha
		if b.muted[name] {
			alpha = 0
		}

		// a zero-sized pad composites at full input size
		w, h := pl.W, pl.H
		if w <= 0 || h <= 0 {
			w, h = hiddenCellSize, hiddenCellSize
		}

		if err := pad.SetProperty("xpos", pl.X); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err := pad.SetProperty("ypos", pl.Y); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err := pad.SetProperty("width", w); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err := pad.SetProperty("height", h); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err := pad.SetProperty("alpha", alpha); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err := pad.SetProperty("zorder", pl.ZOrder); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		b.cellDims[name] = videoDimensions{width: pl.W, height: pl.H}
		if err := b.setCellTargetLocked(name); err != nil {
			return nil, err
		}

		if b.setVideoDimensions != nil && pl.W > 0 && pl.H > 0 {
			d := videoDimensions{width: pl.W, height: pl.H}
			if b.lastDimensions[pl.TrackID] != d {
				b.lastDimensions[pl.TrackID] = d
				pending = append(pending, pendingDimensions{trackID: pl.TrackID, width: pl.W, height: pl.H})
			}
		}
	}

	return pending, nil
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
	// the display is a pixel larger than the output in each dimension, see launchXvfb
	if err = xImageSrc.SetProperty("endx", uint(b.conf.Width-1)); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = xImageSrc.SetProperty("endy", uint(b.conf.Height-1)); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	videoQueue, err := b.buildVideoQueue("video_input_queue")
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
	b.muted = make(map[string]bool)
	b.layoutAlpha = make(map[string]float64)
	b.srcDims = make(map[string]videoDimensions)
	b.cellDims = make(map[string]videoDimensions)
	b.crops = make(map[string]*gst.Element)
	b.inputCaps = make(map[string]*gst.Element)
	b.lastDimensions = make(map[string]videoDimensions)

	if b.conf.VideoDecoding {
		if b.conf.Compositing {
			if err := b.addCompositor(); err != nil {
				return err
			}
		} else {
			if err := b.addSelector(); err != nil {
				return err
			}
		}
		if err := b.addVideoTestSrcBin(); err != nil {
			return err
		}
	}

	if b.conf.Compositing {
		b.layout = NewLayoutManager(b.conf.Layout, int(b.conf.Width), int(b.conf.Height))
	}

	for _, vt := range b.conf.VideoTracks {
		if err := b.addAppSrcBin(vt); err != nil {
			return err
		}
		if b.layout != nil {
			source := TrackSourceCamera
			if vt.PublicationSource == livekit.TrackSource_SCREEN_SHARE {
				source = TrackSourceScreenShare
			}
			pads := b.layout.AddTrack(vt.TrackID, vt.ParticipantIdentity, source)
			if err := b.applyLayout(pads); err != nil {
				return err
			}
		}
	}

	if b.conf.VideoDecoding {
		b.bin.SetGetSrcPad(b.getSrcPad)
		if err := b.addDecodedVideoSink(); err != nil {
			return err
		}
		if !b.conf.Compositing && len(b.conf.VideoTracks) == 0 {
			if err := b.setSelectorPad(videoTestSrcName); err != nil {
				return err
			}
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
		if err = b.createSrcPad(ts.TrackID, name); err != nil {
			return err
		}
	}

	if err = b.bin.AddSourceBin(appSrcBin); err != nil {
		return err
	}

	if b.conf.VideoDecoding {
		return b.setTrackVisible(name, true)
	}

	return nil
}

func (b *VideoBin) attachKeyframeProbe(ts *config.TrackSource, name string, element *gst.Element) error {
	probe, err := newKeyframeProbe(ts.TrackID, ts.MimeType, element, ts.OnKeyframeRequired)
	if err != nil {
		return err
	}
	b.probesMu.Lock()
	b.probes[name] = probe
	b.probesMu.Unlock()
	return nil
}

func (b *VideoBin) closeProbe(name string) {
	b.probesMu.Lock()
	probe, ok := b.probes[name]
	if ok {
		delete(b.probes, name)
	}
	b.probesMu.Unlock()
	if ok {
		probe.Close()
	}
}

func (b *VideoBin) buildAppSrcBin(ts *config.TrackSource, name string) (*gstreamer.Bin, error) {
	appSrcBin := b.bin.NewBin(name)
	appSrcBin.SetEOSFunc(func() bool {
		return false
	})
	ts.AppSrc.SetArg("format", "time")
	if err := ts.AppSrc.SetProperty("is-live", b.conf.Live); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if !b.conf.Live {
		if err := ts.AppSrc.SetProperty("block", true); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}
	if err := appSrcBin.AddElement(ts.AppSrc.Element); err != nil {
		return nil, err
	}

	switch ts.MimeType {
	case types.MimeTypeH264:
		if err := ts.AppSrc.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
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

		h264Parse, err := gst.NewElement("h264parse")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = appSrcBin.AddElement(h264Parse); err != nil {
			return nil, err
		}

		if err := b.attachKeyframeProbe(ts, name, h264Parse); err != nil {
			return nil, err
		}

		if !b.conf.VideoDecoding {
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
		if err := ts.AppSrc.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
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

		if err := b.attachKeyframeProbe(ts, name, rtpVP8Depay); err != nil {
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
		if err := ts.AppSrc.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
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

		vp9Parse, err := gst.NewElement("vp9parse")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = appSrcBin.AddElement(vp9Parse); err != nil {
			return nil, err
		}

		if err := b.attachKeyframeProbe(ts, name, vp9Parse); err != nil {
			return nil, err
		}

		if !b.conf.VideoDecoding {
			vp9Caps, err := gst.NewElement("capsfilter")
			if err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}
			if err = vp9Caps.SetProperty("caps", gst.NewCapsFromString(
				"video/x-vp9,width=[16,2147483647],height=[16,2147483647]",
			)); err != nil {
				return nil, errors.ErrGstPipelineError(err)
			}

			if err = appSrcBin.AddElement(vp9Caps); err != nil {
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

	if err := b.addVideoConverter(appSrcBin, name); err != nil {
		return nil, err
	}

	return appSrcBin, nil
}

// setCellTargetLocked aims an input's crop and scale at the cell it occupies
func (b *VideoBin) setCellTargetLocked(name string) error {
	caps, ok := b.inputCaps[name]
	cell, haveCell := b.cellDims[name]
	if !ok || !haveCell {
		return nil
	}

	// a hidden track runs the same chain as a visible one, so leave it nothing to convert
	if cell.width <= 0 || cell.height <= 0 {
		return b.setInputSize(caps, hiddenCellSize, hiddenCellSize)
	}

	src, haveSrc := b.srcDims[name]
	if !haveSrc || src.width <= 0 || src.height <= 0 {
		return nil
	}

	if err := b.setCoverCropLocked(name, src, cell); err != nil {
		return err
	}

	// I420 needs even dimensions; the compositor pad absorbs the half pixel
	return b.setInputSize(caps, roundUpToEven(cell.width), roundUpToEven(cell.height))
}

func (b *VideoBin) setInputSize(caps *gst.Element, width, height int) error {
	if err := caps.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
		"video/x-raw,framerate=%d/1,format=I420,width=%d,height=%d,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1",
		b.conf.Framerate, width, height,
	))); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	return nil
}

// setCoverCropLocked trims the source to the cell's aspect ratio so the scale can't distort it
func (b *VideoBin) setCoverCropLocked(name string, src, cell videoDimensions) error {
	crop, ok := b.crops[name]
	if !ok {
		return nil
	}

	var left, right, top, bottom int
	if cell.width*src.height > cell.height*src.width {
		keep := src.width * cell.height / cell.width
		top = (src.height - keep) / 2
		bottom = src.height - keep - top
	} else {
		keep := src.height * cell.width / cell.height
		left = (src.width - keep) / 2
		right = src.width - keep - left
	}

	if err := crop.SetProperty("left", left); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := crop.SetProperty("right", right); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := crop.SetProperty("top", top); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := crop.SetProperty("bottom", bottom); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	return nil
}

func roundUpToEven(v int) int {
	return v + v%2
}

func (b *VideoBin) addCompositor() error {
	compositor, err := gst.NewElement("compositor")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	// the gaps between tiles are the template's background, not the compositor's own
	background := "black"
	if isLightLayout(b.conf.Layout) {
		background = "white"
	}
	compositor.SetArg("background", background)
	if err = compositor.SetProperty("latency", uint64(b.conf.Latency.JitterBufferLatency)); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = compositor.SetProperty("min-upstream-latency", uint64(b.conf.Latency.JitterBufferLatency)); err != nil {
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

	if err = b.bin.AddElements(compositor, videoRate, caps); err != nil {
		return err
	}

	b.selector = compositor
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
	if !b.conf.Compositing {
		// hold the test src behind a late-arriving real track so the monotonic PTS
		// probe below doesn't drop the track's first frames. The compositor path
		// doesn't need it - videotestsrc runs continuously underneath the stack.
		if err = queue.SetProperty("min-threshold-time", uint64(videoTestSrcDelay)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
	}

	caps, err := b.newVideoCapsFilter(true)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if err = testSrcBin.AddElements(videoTestSrc, queue, caps); err != nil {
		return err
	}

	pad := b.selector.GetRequestPad("sink_%u")
	if b.conf.Compositing {
		if err = pad.SetProperty("xpos", 0); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = pad.SetProperty("ypos", 0); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = pad.SetProperty("width", int(b.conf.Width)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = pad.SetProperty("height", int(b.conf.Height)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = pad.SetProperty("zorder", uint(0)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = pad.SetProperty("alpha", 1.0); err != nil {
			return errors.ErrGstPipelineError(err)
		}
	} else {
		pad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			pts := uint64(info.GetBuffer().PresentationTimestamp())
			b.mu.Lock()
			if pts < b.lastPTS || b.selectedPad != videoTestSrcName {
				b.mu.Unlock()
				return gst.PadProbeDrop
			}
			b.lastPTS = pts
			b.mu.Unlock()
			return gst.PadProbeOK
		})
	}
	b.pads[videoTestSrcName] = pad
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

		if b.conf.VideoEncoderThreads > 0 {
			if err = x264Enc.SetProperty("threads", b.conf.VideoEncoderThreads); err != nil {
				return errors.ErrGstPipelineError(err)
			}
		}

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

func (b *VideoBin) addVideoConverter(bin *gstreamer.Bin, name string) error {
	videoQueue, err := b.buildVideoQueue("video_input_queue")
	if err != nil {
		return err
	}

	if b.conf.Compositing {
		return b.addCompositorInput(bin, videoQueue, name)
	}

	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	videoScale, err := gst.NewElement("videoscale")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	caps, err := b.newVideoCapsFilter(false)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	return bin.AddElements(videoQueue, videoConvert, videoScale, caps)
}

// addCompositorInput scales to the cell before converting - color conversion costs frame size
func (b *VideoBin) addCompositorInput(bin *gstreamer.Bin, videoQueue *gst.Element, name string) error {
	crop, err := gst.NewElement("videocrop")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	videoScale, err := gst.NewElement("videoscale")
	if err != nil {
		return errors.ErrGstPipelineError(err)
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

	// starts at canvas size; the first layout re-aims it at the cell
	caps, err := b.newVideoCapsFilter(true)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	// without the grace period, frames in flight when the layout moves fail to negotiate
	caps.SetArg("caps-change-mode", "delayed")

	b.mu.Lock()
	b.crops[name] = crop
	b.inputCaps[name] = caps
	b.mu.Unlock()

	// the crop's sink pad is the last place the frame is still at source resolution
	b.watchSrcDimensions(crop.GetStaticPad("sink"), name)

	return bin.AddElements(videoQueue, crop, videoScale, videoConvert, videoRate, caps)
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

func (b *VideoBin) createSrcPad(trackID, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.createSrcPadLocked(trackID, name)
}

func (b *VideoBin) createSrcPadLocked(trackID, name string) error {
	b.names[trackID] = name

	pad := b.selector.GetRequestPad("sink_%u")
	if b.conf.Compositing {
		if err := pad.SetProperty("xpos", 0); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err := pad.SetProperty("ypos", 0); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err := pad.SetProperty("width", int(b.conf.Width)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err := pad.SetProperty("height", int(b.conf.Height)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err := pad.SetProperty("zorder", uint(1)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		// an unmute can arrive before the first layout, and the pad starts opaque
		b.layoutAlpha[name] = 1.0
	} else {
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
	}

	b.pads[name] = pad
	return nil
}

// watchSrcDimensions realigns the crop when the publisher switches layers
func (b *VideoBin) watchSrcDimensions(pad *gst.Pad, name string) {
	pad.AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		event := info.GetEvent()
		if event == nil || event.Type() != gst.EventTypeCaps {
			return gst.PadProbeOK
		}

		dims, ok := dimensionsFromCaps(event.ParseCaps())
		if !ok {
			return gst.PadProbeOK
		}

		b.mu.Lock()
		defer b.mu.Unlock()

		if b.srcDims[name] == dims {
			return gst.PadProbeOK
		}
		b.srcDims[name] = dims
		if err := b.setCellTargetLocked(name); err != nil {
			logger.Warnw("failed to update scale target", err, "name", name)
		}
		return gst.PadProbeOK
	})
}

func dimensionsFromCaps(caps *gst.Caps) (videoDimensions, bool) {
	if caps == nil || caps.GetSize() == 0 {
		return videoDimensions{}, false
	}

	s := caps.GetStructureAt(0)
	if s == nil {
		return videoDimensions{}, false
	}

	width, err := s.GetValue("width")
	if err != nil {
		return videoDimensions{}, false
	}
	height, err := s.GetValue("height")
	if err != nil {
		return videoDimensions{}, false
	}

	w, wOK := width.(int)
	h, hOK := height.(int)
	if !wOK || !hOK {
		return videoDimensions{}, false
	}
	return videoDimensions{width: w, height: h}, true
}

func (b *VideoBin) setTrackVisible(name string, visible bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.setTrackVisibleLocked(name, visible)
}

func (b *VideoBin) setTrackVisibleLocked(name string, visible bool) error {
	if b.conf.Compositing {
		pad, ok := b.pads[name]
		if !ok {
			return errors.New("pad not found: " + name)
		}

		// a layout-hidden pad has width/height 0, which the compositor draws at full input size
		alpha := 0.0
		if visible {
			alpha = b.layoutAlpha[name]
		}
		if err := pad.SetProperty("alpha", alpha); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		// remembered so a later layout recalc doesn't un-mute the track
		b.muted[name] = !visible

		logger.Debugw("track visibility changed", "name", name, "visible", visible)
		return nil
	}

	if visible {
		return b.setSelectorPadLocked(name)
	}
	if b.selectedPad == name {
		return b.setSelectorPadLocked(videoTestSrcName)
	}
	return nil
}

func (b *VideoBin) setSelectorPad(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.setSelectorPadLocked(name)
}

func (b *VideoBin) setSelectorPadLocked(name string) error {
	pad, ok := b.pads[name]
	if !ok {
		return errors.New("pad not found: " + name)
	}

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
