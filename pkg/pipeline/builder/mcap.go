// Copyright 2026 LiveKit, Inc.
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

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/linkdata/deadlock"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	mcapBinName       = "mcap"
	mcapKeepaliveName = "mcap_keepalive"
	mcapTrackField    = "track-id"

	// matches the source bin removal timeout, after which no more samples can arrive from the track
	mcapTrackEndTimeout = 3 * time.Second
)

// MCAPBin funnels one passthrough source bin per track into a single appsink, tagging each with its track
type MCAPBin struct {
	*gstreamer.Bin

	pipeline    *gstreamer.Pipeline
	conf        *config.PipelineConfig
	writeSample func(ts *config.TrackSource, runningTime time.Duration, data []byte) error
	trackEnded  func(trackID string)

	mu     deadlock.Mutex
	nextID int
	names  map[string]string // track ID -> live source bin name
	tracks map[string]*mcapTrack
}

type mcapTrack struct {
	ts            *config.TrackSource
	probe         *keyframeProbe
	stopKeyframes func()
	stopOnce      sync.Once
	removed       atomic.Bool
	eosPassed     atomic.Bool
	endOnce       sync.Once
}

func BuildMCAPBin(
	p *gstreamer.Pipeline,
	conf *config.PipelineConfig,
	writeSample func(*config.TrackSource, time.Duration, []byte) error,
	trackEnded func(string),
) (*MCAPBin, error) {
	b := &MCAPBin{
		Bin:         p.NewBin(mcapBinName),
		pipeline:    p,
		conf:        conf,
		writeSample: writeSample,
		trackEnded:  trackEnded,
		names:       make(map[string]string),
		tracks:      make(map[string]*mcapTrack),
	}

	funnel, err := gst.NewElement("funnel")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	appSink, err := app.NewAppSink()
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	for prop, value := range map[string]bool{"sync": false, "async": false, "enable-last-sample": false} {
		if err = appSink.SetProperty(prop, value); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}
	appSink.SetCallbacks(&app.SinkCallbacks{NewSampleFunc: b.onSample})
	// the funnel must come first: source bin removal releases its request pad on the bin's first element
	if err = b.AddElements(funnel, appSink.Element); err != nil {
		return nil, err
	}

	keepalive, err := buildKeepalive(p)
	if err != nil {
		return nil, err
	}
	if err = p.AddSourceBin(keepalive); err != nil {
		return nil, err
	}
	if err = p.AddSinkBin(b.Bin); err != nil {
		return nil, err
	}

	for _, ts := range append(append([]*config.TrackSource(nil), conf.VideoTracks...), conf.AudioTracks...) {
		if err = b.addTrack(ts); err != nil {
			return nil, err
		}
	}
	p.AddOnTrackAdded(b.onTrackAdded)
	p.AddOnTrackRemoved(b.onTrackRemoved)

	return b, nil
}

// buildKeepalive holds one funnel pad open so a departing last track can't end the file; it only ends with the pipeline
func buildKeepalive(p *gstreamer.Pipeline) (*gstreamer.Bin, error) {
	appSrc, err := app.NewAppSrc()
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	appSrc.SetArg("format", "time")

	keepalive := p.NewBin(mcapKeepaliveName)
	keepalive.SetEOSFunc(func() bool {
		appSrc.EndStream()
		return false
	})
	if err = keepalive.AddElement(appSrc.Element); err != nil {
		return nil, err
	}
	return keepalive, nil
}

// Close stops every track's keyframe requests
func (b *MCAPBin) Close() {
	b.mu.Lock()
	tracks := make([]*mcapTrack, 0, len(b.tracks))
	for _, t := range b.tracks {
		tracks = append(tracks, t)
	}
	b.mu.Unlock()

	for _, t := range tracks {
		t.stop()
	}
}

func (b *MCAPBin) onTrackAdded(ts *config.TrackSource) {
	if b.pipeline.GetState() > gstreamer.StateRunning {
		return
	}
	if err := b.addTrack(ts); err != nil {
		logger.Errorw("failed to add mcap track", err, "trackID", ts.TrackID)
		b.pipeline.OnError(err)
	}
}

func (b *MCAPBin) onTrackRemoved(trackID string) {
	b.mu.Lock()
	name, ok := b.names[trackID]
	delete(b.names, trackID)
	t := b.tracks[name]
	b.mu.Unlock()
	if !ok {
		return
	}

	t.stop()
	// the track drains toward the final EOS, so its remaining samples must still route
	if b.pipeline.GetState() > gstreamer.StateRunning {
		return
	}

	// audio drains before it is removed, so its EOS may already have passed
	t.removed.Store(true)
	if t.eosPassed.Load() {
		b.endTrack(name, t)
	}
	if err := b.RemoveSourceBin(name); err != nil {
		b.pipeline.OnError(err)
	}
	time.AfterFunc(mcapTrackEndTimeout, func() { b.endTrack(name, t) })
}

// endTrack runs once the removed track's last sample has passed, so a republish can take over its topic
func (b *MCAPBin) endTrack(name string, t *mcapTrack) {
	t.endOnce.Do(func() {
		b.mu.Lock()
		delete(b.tracks, name)
		b.mu.Unlock()
		b.trackEnded(t.ts.TrackID)
	})
}

func (b *MCAPBin) addTrack(ts *config.TrackSource) error {
	b.mu.Lock()
	name := fmt.Sprintf("mcap_%s_%d", ts.TrackID, b.nextID)
	b.nextID++
	b.mu.Unlock()

	var (
		bin   *gstreamer.Bin
		probe *keyframeProbe
		caps  string
		err   error
	)
	switch ts.TrackKind {
	case lksdk.TrackKindVideo:
		bin, probe, caps, err = b.buildVideoTrack(ts, name)
	case lksdk.TrackKindAudio:
		bin, caps, err = b.buildAudioTrack(ts, name)
	default:
		err = errors.ErrNotSupported(ts.TrackKind.String())
	}
	if err != nil {
		return err
	}
	tag, err := newTrackCapsFilter(caps, name)
	if err != nil {
		return err
	}
	if err = bin.AddElement(tag); err != nil {
		return err
	}

	t := &mcapTrack{ts: ts, probe: probe}
	tag.GetStaticPad("src").AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if event := info.GetEvent(); event != nil && event.Type() == gst.EventTypeEOS {
			t.eosPassed.Store(true)
			if t.removed.Load() {
				b.endTrack(name, t)
			}
		}
		return gst.PadProbeOK
	})

	// samples can arrive as soon as the bin links
	b.mu.Lock()
	b.tracks[name] = t
	b.mu.Unlock()

	if err = b.AddSourceBin(bin); err != nil {
		b.mu.Lock()
		delete(b.tracks, name)
		b.mu.Unlock()
		t.stop()
		return err
	}
	if ts.TrackKind == lksdk.TrackKindVideo {
		interval := time.Second
		if b.conf.KeyFrameInterval > 0 {
			interval = time.Duration(b.conf.KeyFrameInterval * float64(time.Second))
		}
		t.stopKeyframes = requestKeyframes(interval, ts.OnKeyframeRequested)
	}

	b.mu.Lock()
	b.names[ts.TrackID] = name
	b.mu.Unlock()
	return nil
}

func (b *MCAPBin) buildVideoTrack(ts *config.TrackSource, name string) (*gstreamer.Bin, *keyframeProbe, string, error) {
	var caps string
	switch ts.MimeType {
	case types.MimeTypeH264:
		caps = "video/x-h264,stream-format=byte-stream,alignment=au"
	case types.MimeTypeVP9:
		// SVC layers arrive as separate frames, but each message must hold a whole picture
		caps = "video/x-vp9,alignment=super-frame"
	case types.MimeTypeAV1:
		caps = "video/x-av1"
	default:
		// foxglove CompressedVideo has no VP8 format
		return nil, nil, "", errors.ErrNotSupported(string(ts.MimeType))
	}

	bin, probe, err := buildVideoAppSrcBin(b.Bin, b.conf, ts, name, false)
	if err != nil {
		return nil, nil, "", err
	}
	if ts.MimeType == types.MimeTypeH264 {
		h264Parse, err := gst.NewElement("h264parse")
		if err != nil {
			return nil, nil, "", errors.ErrGstPipelineError(err)
		}
		// SPS/PPS on every IDR lets playback start at any keyframe
		if err = h264Parse.SetProperty("config-interval", -1); err != nil {
			return nil, nil, "", errors.ErrGstPipelineError(err)
		}
		if err = bin.AddElement(h264Parse); err != nil {
			return nil, nil, "", err
		}
	}
	return bin, probe, caps, nil
}

// requestKeyframes asks the publisher for a keyframe every interval until stopped, so passthrough video stays seekable
func requestKeyframes(interval time.Duration, request func()) (stop func()) {
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				request()
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}

func (b *MCAPBin) buildAudioTrack(ts *config.TrackSource, name string) (*gstreamer.Bin, string, error) {
	bin := b.NewBin(name)
	bin.SetEOSFunc(func() bool {
		return false
	})
	ts.AppSrc.SetArg("format", "time")
	if err := ts.AppSrc.SetProperty("is-live", b.conf.Live); err != nil {
		return nil, "", errors.ErrGstPipelineError(err)
	}
	if !b.conf.Live {
		if err := ts.AppSrc.SetProperty("block", true); err != nil {
			return nil, "", errors.ErrGstPipelineError(err)
		}
	}
	if err := bin.AddElement(ts.AppSrc.Element); err != nil {
		return nil, "", err
	}

	var caps string
	var names []string
	switch {
	case b.conf.OpusPassthrough(ts.MimeType):
		channels := 1
		if ts.Stereo {
			channels = 2
		}
		caps = fmt.Sprintf("audio/x-opus,rate=48000,channels=%d,channel-mapping-family=0", channels)
	case ts.MimeType == types.MimeTypePCMU:
		caps = fmt.Sprintf("application/x-rtp,media=audio,payload=%d,encoding-name=PCMU,clock-rate=%d", ts.PayloadType, ts.ClockRate)
		names = []string{"rtppcmudepay", "mulawdec", "audioconvert", "audioresample", "opusenc"}
	case ts.MimeType == types.MimeTypePCMA:
		caps = fmt.Sprintf("application/x-rtp,media=audio,payload=%d,encoding-name=PCMA,clock-rate=%d", ts.PayloadType, ts.ClockRate)
		names = []string{"rtppcmadepay", "alawdec", "audioconvert", "audioresample", "opusenc"}
	default:
		return nil, "", errors.ErrNotSupported(string(ts.MimeType))
	}
	names = append(names, "opusparse")
	if err := ts.AppSrc.SetProperty("caps", gst.NewCapsFromString(caps)); err != nil {
		return nil, "", errors.ErrGstPipelineError(err)
	}
	for _, elementName := range names {
		element, err := gst.NewElement(elementName)
		if err != nil {
			return nil, "", errors.ErrGstPipelineError(err)
		}
		if err = bin.AddElement(element); err != nil {
			return nil, "", err
		}
	}
	return bin, "audio/x-opus", nil
}

func (b *MCAPBin) onSample(sink *app.Sink) gst.FlowReturn {
	sample := sink.PullSample()
	ts, ok := b.sampleTrack(sample)
	if !ok {
		return gst.FlowOK
	}

	buffer := sample.GetBuffer()
	pts := buffer.PresentationTimestamp()
	if pts == gst.ClockTimeNone {
		return gst.FlowOK
	}
	// the funnel re-sends each track's segment when it switches pads, so this is the sample's own track segment
	runningTime := gst.ClockTime(sample.GetSegment().ToRunningTime(gst.FormatTime, uint64(pts))).AsDuration()
	if runningTime == nil {
		return gst.FlowOK
	}

	mapped := buffer.Map(gst.MapRead)
	defer buffer.Unmap()
	if err := b.writeSample(ts, *runningTime, mapped.Bytes()); err != nil {
		b.pipeline.OnError(err)
		return gst.FlowError
	}
	return gst.FlowOK
}

// sampleTrack is false for a straggler from a track that already ended on its timeout
func (b *MCAPBin) sampleTrack(sample *gst.Sample) (*config.TrackSource, bool) {
	value, _ := sample.GetCaps().GetStructureAt(0).GetValue(mcapTrackField)
	name, _ := value.(string)

	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tracks[name]
	if !ok {
		return nil, false
	}
	return t.ts, true
}

func (t *mcapTrack) stop() {
	t.stopOnce.Do(func() {
		if t.probe != nil {
			t.probe.Close()
		}
		if t.stopKeyframes != nil {
			t.stopKeyframes()
		}
	})
}

// newTrackCapsFilter adds the track's source bin name to its caps, which the funnel forwards whenever it switches tracks
func newTrackCapsFilter(caps, name string) (*gst.Element, error) {
	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	tagged := fmt.Sprintf("%s,%s=(string)%q", caps, mcapTrackField, name)
	if err = capsFilter.SetProperty("caps", gst.NewCapsFromString(tagged)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	return capsFilter, nil
}
