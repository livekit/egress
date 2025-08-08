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
	"bytes"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	audioChannelStereo = 0
	audioChannelLeft   = 1
	audioChannelRight  = 2
)

type AudioBin struct {
	bin  *gstreamer.Bin
	conf *config.PipelineConfig

	mu          sync.Mutex
	nextID      int
	nextChannel int
	names       map[string]string

	audioMixerDebug *AudioMixerDebug
}

type AudioMixerDebug struct {
	mixer     *gst.Element
	ptsRanges map[string][]GstTimeInfo
	lock      sync.Mutex
}

type GstTimeInfo struct {
	pts      gst.ClockTime
	duration gst.ClockTime
}

func (amd *AudioMixerDebug) MergeRanges(padName string, newRange GstTimeInfo) {
	ranges := amd.ptsRanges[padName]
	start := newRange.pts
	end := start + newRange.duration

	merged := []GstTimeInfo{}

	for _, r := range ranges {
		rStart := r.pts
		rEnd := rStart + r.duration

		// Check for overlap or consecutive (touching) ranges
		if end < rStart || start > rEnd {
			// No overlap
			merged = append(merged, r)
			continue
		}

		// Merge overlapping/consecutive ranges
		start = min(start, rStart)
		end = max(end, rEnd)
	}

	// Insert the new (merged) range
	merged = append(merged, GstTimeInfo{
		pts:      start,
		duration: end - start,
	})

	// Update the pad's range list (re-sort to help future merges if needed)
	amd.ptsRanges[padName] = sortAndMerge(merged)
}

func gstRangesToString(ranges []GstTimeInfo) string {
	if len(ranges) == 0 {
		return "[]"
	}

	var str string
	for _, r := range ranges {
		str += fmt.Sprintf("{pts: %s, duration: %s}, ", r.pts.String(), r.duration.String())
	}
	return "[" + str[:len(str)-2] + "]"
}

func sortAndMerge(ranges []GstTimeInfo) []GstTimeInfo {
	if len(ranges) == 0 {
		return nil
	}

	// Sort by pts
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].pts < ranges[j].pts
	})

	merged := []GstTimeInfo{ranges[0]}
	for i := 1; i < len(ranges); i++ {
		last := merged[len(merged)-1]
		curr := ranges[i]

		lastStart := last.pts
		lastEnd := lastStart + last.duration
		currStart := curr.pts
		currEnd := currStart + curr.duration

		if currStart <= lastEnd { // overlap or consecutive
			merged[len(merged)-1] = GstTimeInfo{
				pts:      min(lastStart, currStart),
				duration: max(lastEnd, currEnd) - min(lastStart, currStart),
			}
		} else {
			merged = append(merged, curr)
		}
	}
	return merged
}

func (amd *AudioMixerDebug) IsRangeCoveredBySinks(srcRange GstTimeInfo) bool {
	srcStart := srcRange.pts
	srcEnd := srcStart + srcRange.duration

	for sinkPad, ranges := range amd.ptsRanges {
		if !strings.HasPrefix(sinkPad, "sink") {
			continue // skip non-sink pads
		}

		if len(ranges) == 0 {
			return false // no data on this sink pad
		}

		covered := false
		for _, r := range ranges {
			rStart := r.pts
			rEnd := rStart + r.duration

			if srcStart >= rStart && srcEnd <= rEnd {
				covered = true
				break
			}
		}

		if !covered {
			return false // this sink pad does not fully cover the src range
		}
	}

	return true // all sink pads cover the src range
}

// GetGoid extracts the goroutine ID from the stack trace.
func getGoid() uint64 {
	b := make([]byte, 64)
	b = b[:runtime.Stack(b, false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	b = b[:bytes.IndexByte(b, ' ')]
	n, _ := strconv.ParseUint(string(b), 10, 64)
	return n
}

func BuildAudioBin(pipeline *gstreamer.Pipeline, p *config.PipelineConfig) error {
	b := &AudioBin{
		bin:   pipeline.NewBin("audio"),
		conf:  p,
		names: make(map[string]string),
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
	}

	if len(p.GetEncodedOutputs()) > 1 {
		tee, err := gst.NewElementWithName("tee", "audio_tee")
		if err != nil {
			return err
		}
		if err = b.bin.AddElement(tee); err != nil {
			return err
		}
	} else {
		queue, err := gstreamer.BuildQueue("audio_queue", p.Latency.PipelineLatency, true)
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = b.bin.AddElement(queue); err != nil {
			return err
		}
	}

	return pipeline.AddSourceBin(b.bin)
}

func (b *AudioBin) onTrackAdded(ts *config.TrackSource) {
	if b.bin.GetState() > gstreamer.StateRunning {
		return
	}

	if ts.TrackKind == lksdk.TrackKindAudio {
		if err := b.addAudioAppSrcBin(ts); err != nil {
			b.bin.OnError(err)
		}
	}
}

func (b *AudioBin) onTrackRemoved(trackID string) {
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
	b.mu.Unlock()

	if err := b.bin.RemoveSourceBin(name); err != nil {
		b.bin.OnError(err)
	}
}

func (b *AudioBin) buildWebInput() error {
	logger.Infow("*** Building Web input")
	pulseSrc, err := gst.NewElement("pulsesrc")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = pulseSrc.SetProperty("device", fmt.Sprintf("%s.monitor", b.conf.Info.EgressId)); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = b.bin.AddElement(pulseSrc); err != nil {
		return err
	}

	if err = addAudioConverter(b.bin, b.conf, audioChannelStereo); err != nil {
		return err
	}
	if b.conf.AudioTranscoding {
		if err = b.addEncoder(); err != nil {
			return err
		}
	}

	return nil
}

func (b *AudioBin) buildSDKInput() error {
	logger.Infow("*** Building sdk input")
	for _, tr := range b.conf.AudioTracks {
		if err := b.addAudioAppSrcBin(tr); err != nil {
			return err
		}
	}
	if err := b.addAudioTestSrcBin(); err != nil {
		return err
	}
	if err := b.addMixer(); err != nil {
		return err
	}
	if b.conf.AudioTranscoding {
		if err := b.addEncoder(); err != nil {
			return err
		}
	}

	return nil
}

func (b *AudioBin) addAudioAppSrcBin(ts *config.TrackSource) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	name := fmt.Sprintf("%s_%d", ts.TrackID, b.nextID)
	b.nextID++
	b.names[ts.TrackID] = name

	appSrcBin := b.bin.NewBin(name)
	appSrcBin.SetEOSFunc(func() bool {
		return false
	})
	ts.AppSrc.Element.SetArg("format", "time")
	if err := ts.AppSrc.Element.SetProperty("is-live", true); err != nil {
		return err
	}
	if err := appSrcBin.AddElement(ts.AppSrc.Element); err != nil {
		return err
	}

	switch ts.MimeType {
	case types.MimeTypeOpus:
		if err := ts.AppSrc.Element.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=audio,payload=%d,encoding-name=OPUS,clock-rate=%d",
			ts.PayloadType, ts.ClockRate,
		))); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		rtpOpusDepay, err := gst.NewElement("rtpopusdepay")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		opusDec, err := gst.NewElement("opusdec")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		if err = appSrcBin.AddElements(rtpOpusDepay, opusDec); err != nil {
			return err
		}

	default:
		return errors.ErrNotSupported(string(ts.MimeType))
	}

	if err := addAudioConverter(appSrcBin, b.conf, b.getChannel(ts)); err != nil {
		return err
	}

	if err := b.bin.AddSourceBin(appSrcBin); err != nil {
		return err
	}

	return nil
}

func (b *AudioBin) getChannel(ts *config.TrackSource) int {
	switch b.conf.AudioMixing {
	case livekit.AudioMixing_DEFAULT_MIXING:
		return audioChannelStereo

	case livekit.AudioMixing_DUAL_CHANNEL_AGENT:
		if ts.ParticipantKind == lksdk.ParticipantAgent {
			return audioChannelLeft
		} else {
			return audioChannelRight
		}

	case livekit.AudioMixing_DUAL_CHANNEL_ALTERNATE:
		next := b.nextChannel
		b.nextChannel++
		return next%2 + 1
	}

	return audioChannelStereo
}

func (b *AudioBin) addAudioTestSrcBin() error {
	testSrcBin := b.bin.NewBin("audio_test_src")
	if err := b.bin.AddSourceBin(testSrcBin); err != nil {
		return err
	}

	audioTestSrc, err := gst.NewElement("audiotestsrc")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = audioTestSrc.SetProperty("volume", 0.0); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = audioTestSrc.SetProperty("do-timestamp", true); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = audioTestSrc.SetProperty("is-live", true); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	audioCaps, err := newAudioCapsFilter(b.conf, audioChannelStereo)
	if err != nil {
		return err
	}

	return testSrcBin.AddElements(audioTestSrc, audioCaps)
}

func (b *AudioBin) addMixer() error {
	logger.Infow("*** Adding audio mixer")
	audioMixer, err := gst.NewElement("audiomixer")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = audioMixer.SetProperty("latency", uint64(b.conf.Latency.AudioMixerLatency)); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = audioMixer.SetProperty("alignment-threshold", uint64(b.conf.Latency.PipelineLatency)); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	mixedCaps, err := newAudioCapsFilter(b.conf, audioChannelStereo)
	if err != nil {
		return err
	}

	b.audioMixerDebug = &AudioMixerDebug{
		mixer:     audioMixer,
		ptsRanges: make(map[string][]GstTimeInfo),
	}

	audioMixer.Connect("pad-added", func(el *gst.Element, pad *gst.Pad) {
		padName := pad.GetName()
		fmt.Printf("***Pad added: %s", padName)
		pad.AddProbe(gst.PadProbeTypeBuffer, func(p *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			buffer := info.GetBuffer()
			if buffer == nil {
				return gst.PadProbeOK
			}
			fmt.Printf("***[%d]<%s>Buffer pts: %s, duration: %s", getGoid(), padName, buffer.PresentationTimestamp().String(), buffer.Duration().String())
			if buffer.Duration() == gst.ClockTimeNone {
				fmt.Printf("***<%s>Buffer duration is None", padName)
				return gst.PadProbeOK
			}

			if b.audioMixerDebug == nil {
				fmt.Printf("*** audioMixerDebug is nil! ***\n")
			}

			b.audioMixerDebug.lock.Lock()
			defer b.audioMixerDebug.lock.Unlock()

			ranges := b.audioMixerDebug.ptsRanges[padName]
			var prevPts gst.ClockTime
			var prevDuration gst.ClockTime
			if len(ranges) > 0 {
				prevPts = ranges[len(ranges)-1].pts
				prevDuration = ranges[len(ranges)-1].duration
			}

			if prevPts > 0 && (buffer.PresentationTimestamp() != prevPts+prevDuration) {
				fmt.Printf("***<%s>Discontinuity discovered at: %s", padName, buffer.PresentationTimestamp().String())
				fmt.Printf("***<%s>Existing ranges: %s", padName, gstRangesToString(ranges))
			}
			b.audioMixerDebug.MergeRanges(padName, GstTimeInfo{
				pts:      buffer.PresentationTimestamp(),
				duration: buffer.Duration(),
			})
			return gst.PadProbeOK
		})
	})
	srcPad := audioMixer.GetStaticPad("src")
	srcPad.AddProbe(gst.PadProbeTypeBuffer, func(p *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		fmt.Printf("***<[%d]%s>Buffer pts %s, duration: %s", getGoid(), srcPad.GetName(), info.GetBuffer().PresentationTimestamp().String(), info.GetBuffer().Duration().String())
		buffer := info.GetBuffer()
		if buffer == nil {
			return gst.PadProbeOK
		}

		b.audioMixerDebug.lock.Lock()
		defer b.audioMixerDebug.lock.Unlock()

		if b.audioMixerDebug == nil {
			fmt.Printf("*** audioMixerDebug is nil! ***\n")
		}

		gstTimeinfo := GstTimeInfo{
			pts:      buffer.PresentationTimestamp(),
			duration: buffer.Duration(),
		}

		if !b.audioMixerDebug.IsRangeCoveredBySinks(gstTimeinfo) {
			fmt.Printf("***<%s>Source range not covered by sinks: %s", srcPad.GetName(), buffer.PresentationTimestamp().String())
		}

		b.audioMixerDebug.MergeRanges(srcPad.GetName(), gstTimeinfo)
		return gst.PadProbeOK
	})
	// probes should be removed but conciously not doing it - debugging only

	return b.bin.AddElements(audioMixer, mixedCaps)
}

func (b *AudioBin) addEncoder() error {
	switch b.conf.AudioOutCodec {
	case types.MimeTypeOpus:
		opusEnc, err := gst.NewElement("opusenc")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = opusEnc.SetProperty("bitrate", int(b.conf.AudioBitrate*1000)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		return b.bin.AddElement(opusEnc)

	case types.MimeTypeAAC:
		faac, err := gst.NewElement("faac")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = faac.SetProperty("bitrate", int(b.conf.AudioBitrate*1000)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		return b.bin.AddElement(faac)

	case types.MimeTypeRawAudio:
		return nil

	default:
		return errors.ErrNotSupported(string(b.conf.AudioOutCodec))
	}
}

func addAudioConverter(b *gstreamer.Bin, p *config.PipelineConfig, channel int) error {
	audioQueue, err := gstreamer.BuildQueue("audio_input_queue", p.Latency.PipelineLatency, true)
	if err != nil {
		return err
	}

	audioConvert, err := gst.NewElement("audioconvert")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	audioResample, err := gst.NewElement("audioresample")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	capsFilter, err := newAudioCapsFilter(p, channel)
	if err != nil {
		return err
	}

	return b.AddElements(audioQueue, audioConvert, audioResample, capsFilter)
}

func newAudioCapsFilter(p *config.PipelineConfig, channel int) (*gst.Element, error) {
	var channelCaps string
	if channel == audioChannelStereo {
		channelCaps = "channels=2"
	} else {
		channelCaps = fmt.Sprintf("channels=1,channel-mask=(bitmask)0x%d", channel)
	}

	var caps *gst.Caps
	switch p.AudioOutCodec {
	case types.MimeTypeOpus, types.MimeTypeRawAudio:
		caps = gst.NewCapsFromString(fmt.Sprintf(
			"audio/x-raw,format=S16LE,layout=interleaved,rate=48000,%s",
			channelCaps,
		))
	case types.MimeTypeAAC:
		caps = gst.NewCapsFromString(fmt.Sprintf(
			"audio/x-raw,format=S16LE,layout=interleaved,rate=%d,%s",
			p.AudioFrequency, channelCaps,
		))
	default:
		return nil, errors.ErrNotSupported(string(p.AudioOutCodec))
	}

	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = capsFilter.SetProperty("caps", caps); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return capsFilter, nil
}
