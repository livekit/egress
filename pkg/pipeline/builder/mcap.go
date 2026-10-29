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
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/linkdata/deadlock"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// the track worker drains a removed video track only after OnTrackRemoved returns, so its EOS arrives late
const mcapBranchEOSTimeout = 5 * time.Second

type branchExit int

const (
	exitEOS        branchExit = iota // final EOS reaches the appsink, which posts it on the bus
	exitDetach                       // removed from the running pipeline
	exitNeverAdded                   // the pipeline was past running when the branch was built
)

// MCAPBin runs one self-contained appsrc → appsink branch per track, so tracks are never mixed
type MCAPBin struct {
	pipeline    *gstreamer.Pipeline
	conf        *config.PipelineConfig
	writeSample func(ts *config.TrackSource, runningTime time.Duration, data []byte) error
	trackEnded  func(trackID string)

	mu        deadlock.Mutex
	nextID    int
	names     map[string]string
	branches  map[string]*mcapBranch
	detaching int
	ending    bool
	eosPassed bool // some appsink received the final EOS, so the bus will post EOS itself
	drained   core.Fuse
}

type mcapBranch struct {
	ts            *config.TrackSource
	probe         *keyframeProbe
	stopKeyframes func()
}

func BuildMCAPBin(
	p *gstreamer.Pipeline,
	conf *config.PipelineConfig,
	writeSample func(*config.TrackSource, time.Duration, []byte) error,
	trackEnded func(string),
) (*MCAPBin, error) {
	b := &MCAPBin{
		pipeline:    p,
		conf:        conf,
		writeSample: writeSample,
		trackEnded:  trackEnded,
		names:       make(map[string]string),
		branches:    make(map[string]*mcapBranch),
	}

	for _, ts := range append(append([]*config.TrackSource(nil), conf.VideoTracks...), conf.AudioTracks...) {
		if err := b.addBranch(ts); err != nil {
			return nil, err
		}
	}
	p.AddOnTrackAdded(b.onTrackAdded)
	p.AddOnTrackRemoved(b.onTrackRemoved)

	return b, nil
}

// MarkEnding makes Drained report true once every branch has seen EOS and been removed
func (b *MCAPBin) MarkEnding() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ending = true
	b.drainLocked()
}

func (b *MCAPBin) Drained() bool {
	return b.drained.IsBroken()
}

func (b *MCAPBin) onTrackAdded(ts *config.TrackSource) {
	if b.pipeline.GetState() > gstreamer.StateRunning {
		return
	}
	if err := b.addBranch(ts); err != nil {
		logger.Errorw("failed to add mcap branch", err, "trackID", ts.TrackID)
		b.pipeline.OnError(err)
	}
}

func (b *MCAPBin) onTrackRemoved(trackID string) {
	b.mu.Lock()
	name, ok := b.names[trackID]
	delete(b.names, trackID)
	b.mu.Unlock()
	if ok {
		time.AfterFunc(mcapBranchEOSTimeout, func() { b.removeBranch(name, exitDetach) })
	}
}

func (b *MCAPBin) addBranch(ts *config.TrackSource) error {
	b.mu.Lock()
	name := fmt.Sprintf("mcap_%s_%d", ts.TrackID, b.nextID)
	b.nextID++
	b.mu.Unlock()

	appSink, err := app.NewAppSink()
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	for prop, value := range map[string]bool{"sync": false, "async": false, "enable-last-sample": false} {
		if err = appSink.SetProperty(prop, value); err != nil {
			return errors.ErrGstPipelineError(err)
		}
	}
	appSink.SetCallbacks(&app.SinkCallbacks{
		NewSampleFunc: func(sink *app.Sink) gst.FlowReturn {
			return b.onSample(sink, ts)
		},
	})
	appSink.GetStaticPad("sink").AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if event := info.GetEvent(); event == nil || event.Type() != gst.EventTypeEOS {
			return gst.PadProbeOK
		}

		b.mu.Lock()
		ending := b.ending
		b.mu.Unlock()
		if ending {
			b.removeBranch(name, exitEOS)
			return gst.PadProbeOK
		}
		go b.removeBranch(name, exitDetach)
		// an appsink that sees EOS counts toward pipeline EOS, which would end the egress when this was the last track
		return gst.PadProbeDrop
	})

	var (
		bin   *gstreamer.Bin
		probe *keyframeProbe
	)
	switch ts.TrackKind {
	case lksdk.TrackKindVideo:
		bin, probe, err = b.buildVideoBranch(ts, name)
	case lksdk.TrackKindAudio:
		bin, err = b.buildAudioBranch(ts, name)
	default:
		err = errors.ErrNotSupported(ts.TrackKind.String())
	}
	if err != nil {
		return err
	}
	if err = bin.AddElement(appSink.Element); err != nil {
		return err
	}

	branch := &mcapBranch{ts: ts, probe: probe}
	if ts.TrackKind == lksdk.TrackKindVideo {
		interval := time.Second
		if b.conf.KeyFrameInterval > 0 {
			interval = time.Duration(b.conf.KeyFrameInterval * float64(time.Second))
		}
		branch.stopKeyframes = requestKeyframes(interval, ts.OnKeyframeRequested)
	}

	b.mu.Lock()
	b.names[ts.TrackID] = name
	b.branches[name] = branch
	b.mu.Unlock()

	added, err := b.pipeline.AddSiblingBin(bin)
	if !added {
		b.removeBranch(name, exitNeverAdded)
	}
	return err
}

func (b *MCAPBin) buildVideoBranch(ts *config.TrackSource, name string) (*gstreamer.Bin, *keyframeProbe, error) {
	switch ts.MimeType {
	case types.MimeTypeH264, types.MimeTypeVP9, types.MimeTypeAV1:
	default:
		// foxglove CompressedVideo has no VP8 format
		return nil, nil, errors.ErrNotSupported(string(ts.MimeType))
	}

	bin, probe, err := buildVideoAppSrcBin(b.pipeline.Bin, b.conf, ts, name, false)
	if err != nil {
		return nil, nil, err
	}

	switch ts.MimeType {
	case types.MimeTypeH264:
		h264Parse, err := gst.NewElement("h264parse")
		if err != nil {
			return nil, nil, errors.ErrGstPipelineError(err)
		}
		// SPS/PPS on every IDR lets playback start at any keyframe
		if err = h264Parse.SetProperty("config-interval", -1); err != nil {
			return nil, nil, errors.ErrGstPipelineError(err)
		}
		outputCaps, err := newCapsFilter("video/x-h264,stream-format=byte-stream,alignment=au")
		if err != nil {
			return nil, nil, err
		}
		if err = bin.AddElements(h264Parse, outputCaps); err != nil {
			return nil, nil, err
		}
	case types.MimeTypeVP9:
		// SVC layers arrive as separate frames, but each message must hold a whole picture
		superFrames, err := newCapsFilter("video/x-vp9,alignment=super-frame")
		if err != nil {
			return nil, nil, err
		}
		if err = bin.AddElement(superFrames); err != nil {
			return nil, nil, err
		}
	}
	return bin, probe, nil
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

func (b *MCAPBin) buildAudioBranch(ts *config.TrackSource, name string) (*gstreamer.Bin, error) {
	bin := b.pipeline.NewBin(name)
	bin.SetEOSFunc(func() bool {
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
	if err := bin.AddElement(ts.AppSrc.Element); err != nil {
		return nil, err
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
		return nil, errors.ErrNotSupported(string(ts.MimeType))
	}
	names = append(names, "opusparse")
	if err := ts.AppSrc.SetProperty("caps", gst.NewCapsFromString(caps)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	for _, elementName := range names {
		element, err := gst.NewElement(elementName)
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = bin.AddElement(element); err != nil {
			return nil, err
		}
	}
	return bin, nil
}

func (b *MCAPBin) onSample(sink *app.Sink, ts *config.TrackSource) gst.FlowReturn {
	sample := sink.PullSample()
	buffer := sample.GetBuffer()
	pts := buffer.PresentationTimestamp()
	if pts == gst.ClockTimeNone {
		return gst.FlowOK
	}
	// a buffer before the segment start has no running time
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

func (b *MCAPBin) removeBranch(name string, exit branchExit) {
	b.mu.Lock()
	branch, ok := b.branches[name]
	if !ok {
		b.mu.Unlock()
		return
	}
	delete(b.branches, name)
	switch exit {
	case exitEOS:
		b.eosPassed = true
	case exitDetach:
		b.detaching++
	}
	b.mu.Unlock()

	if branch.probe != nil {
		branch.probe.Close()
	}
	if branch.stopKeyframes != nil {
		branch.stopKeyframes()
	}
	if exit == exitDetach {
		if err := b.pipeline.RemoveSiblingBin(name); err != nil {
			b.pipeline.OnError(err)
		}
	}

	b.mu.Lock()
	if exit == exitDetach {
		b.detaching--
	}
	trackLive := false
	for _, other := range b.branches {
		trackLive = trackLive || other.ts.TrackID == branch.ts.TrackID
	}
	b.drainLocked()
	b.mu.Unlock()

	if !trackLive {
		b.trackEnded(branch.ts.TrackID)
	}
}

// drainLocked must wait for in-flight detaches: a bus EOS stops the GLib loop before their removal callbacks run
func (b *MCAPBin) drainLocked() {
	if !b.ending || len(b.branches) > 0 || b.detaching > 0 || b.drained.IsBroken() {
		return
	}
	b.drained.Break()
	if !b.eosPassed {
		b.pipeline.PostEOS()
	}
}

func newCapsFilter(caps string) (*gst.Element, error) {
	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = capsFilter.SetProperty("caps", gst.NewCapsFromString(caps)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	return capsFilter, nil
}
