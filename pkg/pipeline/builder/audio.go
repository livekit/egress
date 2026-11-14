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
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/audio"
	"github.com/linkdata/deadlock"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/tempo"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	leakyQueue    = true
	blockingQueue = false

	audioRateTolerance = 3 * time.Millisecond
	audioBinName       = "audio"
)

type AudioBin struct {
	bin  *gstreamer.Bin
	conf *config.PipelineConfig

	mu          deadlock.Mutex
	nextID      int
	nextChannel livekit.AudioChannel
	names       map[string]string
}

func BuildAudioBin(pipeline *gstreamer.Pipeline, p *config.PipelineConfig) error {
	b := &AudioBin{
		bin:   pipeline.NewBin(audioBinName),
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
		tee, err := gst.NewElementWithName("tee", fmt.Sprintf("%s_tee", audioBinName))
		if err != nil {
			return err
		}
		if err = b.bin.AddElement(tee); err != nil {
			return err
		}
	} else {
		latency := p.Latency.PipelineLatency
		if p.SourceType == types.SourceTypeSDK && p.VideoEnabled {
			latency += videoTestSrcDelay
		}
		queue, err := gstreamer.BuildQueue(fmt.Sprintf("%s_queue", audioBinName), latency, p.Live)
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
		logger.Debugw("adding audio app src bin", "trackID", ts.TrackID)
		if err := b.addAudioAppSrcBin(ts); err != nil {
			logger.Errorw("failed to add audio app src bin", err, "trackID", ts.TrackID)
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

	if err = addAudioConverter(b.bin, b.conf, livekit.AudioChannel_AUDIO_CHANNEL_BOTH, leakyQueue); err != nil {
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
	for _, tr := range b.conf.AudioTracks {
		if err := b.addAudioAppSrcBin(tr); err != nil {
			return err
		}
	}
	if !b.conf.AudioTranscoding {
		return nil
	}
	if b.conf.Live {
		if err := b.addAudioTestSrcBin(); err != nil {
			return err
		}
	} else {
		// a non-live mixer with only track pads stops at the last track's end and outputs nothing before the first
		if b.conf.AudioFillerSrc == nil {
			return errors.New("non-live audio mixing requires AudioFillerSrc")
		}
		if err := b.addAudioFillerSrcBin(); err != nil {
			return err
		}
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

	return b.addAudioAppSrcBinLocked(ts)
}

func (b *AudioBin) addAudioAppSrcBinLocked(ts *config.TrackSource) error {
	name := fmt.Sprintf("%s_%d", ts.TrackID, b.nextID)
	b.nextID++
	b.names[ts.TrackID] = name

	appSrcBin := b.bin.NewBin(name)
	appSrcBin.SetEOSFunc(func() bool {
		return false
	})
	ts.AppSrc.SetArg("format", "time")
	if err := ts.AppSrc.SetProperty("is-live", b.conf.Live); err != nil {
		return err
	}
	if !b.conf.Live {
		if err := ts.AppSrc.SetProperty("block", true); err != nil {
			return err
		}
	}
	if err := appSrcBin.AddElement(ts.AppSrc.Element); err != nil {
		return err
	}

	if !b.conf.AudioTranscoding {
		return b.addOpusPassthroughLocked(appSrcBin, ts)
	}

	switch ts.MimeType {
	case types.MimeTypeOpus:
		if err := ts.AppSrc.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
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

	case types.MimeTypePCMU:
		if err := ts.AppSrc.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=audio,payload=%d,encoding-name=PCMU,clock-rate=%d",
			ts.PayloadType, ts.ClockRate,
		))); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		rtpPCMUDepay, err := gst.NewElement("rtppcmudepay")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		mulawDec, err := gst.NewElement("mulawdec")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		if err = appSrcBin.AddElements(rtpPCMUDepay, mulawDec); err != nil {
			return err
		}

	case types.MimeTypePCMA:
		if err := ts.AppSrc.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=audio,payload=%d,encoding-name=PCMA,clock-rate=%d",
			ts.PayloadType, ts.ClockRate,
		))); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		rtpPCMADepay, err := gst.NewElement("rtppcmadepay")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		alawDec, err := gst.NewElement("alawdec")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		if err = appSrcBin.AddElements(rtpPCMADepay, alawDec); err != nil {
			return err
		}

	default:
		return errors.ErrNotSupported(string(ts.MimeType))
	}

	var pacer *audioPacer
	if b.conf.AudioTempoController.Enabled {
		p, err := b.addAudioConvertWithPitch(appSrcBin, b.conf, b.getChannelLocked(ts), blockingQueue)
		if err != nil {
			return err
		}
		pacer = p
	} else {
		if err := addAudioConverter(appSrcBin, b.conf, b.getChannelLocked(ts), blockingQueue); err != nil {
			return err
		}
	}

	if err := b.bin.AddSourceBin(appSrcBin); err != nil {
		return err
	}

	if pacer != nil && ts.TempoController != nil {
		// tc must be set before callbacks: both replay current state synchronously.
		pacer.tc = ts.TempoController
		trackID := ts.TrackID
		ts.TempoController.OnTierChange(func(tier tempo.Tier) {
			switch tier {
			case tempo.TierNormal:
				logger.Infow("audio drift back inside soft budget", "trackID", trackID, "tier", tier)
				pacer.brake()
			case tempo.TierSoft:
				logger.Warnw("audio drift exceeded soft budget, boosting pacer rate", nil,
					"trackID", trackID, "tier", tier)
				pacer.boost()
			case tempo.TierHard:
				// Hard tier means the pacer cannot absorb drift fast enough even with
				// boosting; downstream is approaching mixer alignment-threshold and
				// will start dropping
				logger.Warnw("audio drift exceeded hard budget",
					errors.New("uncorrected drift above hard budget"),
					"trackID", trackID, "tier", tier)
				pacer.boost()
			}
		})
		ts.TempoController.OnDriftDetectedCallback(func(drift time.Duration) {
			logger.Debugw("starting audio pacer to cover the drift", "drift", drift)
			pacer.start(drift)
		})
	}

	return nil
}

func (b *AudioBin) addOpusPassthroughLocked(appSrcBin *gstreamer.Bin, ts *config.TrackSource) error {
	channels := 1
	if ts.Stereo {
		channels = 2
	}
	if err := ts.AppSrc.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
		"audio/x-opus,rate=48000,channels=%d,channel-mapping-family=0", channels,
	))); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	opusParse, err := gst.NewElement("opusparse")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = appSrcBin.AddElement(opusParse); err != nil {
		return err
	}

	return b.bin.AddSourceBin(appSrcBin)
}

func (b *AudioBin) getChannelLocked(ts *config.TrackSource) livekit.AudioChannel {
	if ts.AudioChannel != nil {
		return *ts.AudioChannel
	}

	switch b.conf.AudioMixing {
	case livekit.AudioMixing_DEFAULT_MIXING:
		return livekit.AudioChannel_AUDIO_CHANNEL_BOTH

	case livekit.AudioMixing_DUAL_CHANNEL_AGENT:
		if ts.ParticipantKind == lksdk.ParticipantAgent {
			return livekit.AudioChannel_AUDIO_CHANNEL_LEFT
		}
		return livekit.AudioChannel_AUDIO_CHANNEL_RIGHT

	case livekit.AudioMixing_DUAL_CHANNEL_ALTERNATE:
		if b.nextChannel == livekit.AudioChannel_AUDIO_CHANNEL_LEFT {
			b.nextChannel = livekit.AudioChannel_AUDIO_CHANNEL_RIGHT
		} else {
			b.nextChannel = livekit.AudioChannel_AUDIO_CHANNEL_LEFT
		}
		return b.nextChannel
	}

	return livekit.AudioChannel_AUDIO_CHANNEL_BOTH
}

func (b *AudioBin) addAudioTestSrcBin() error {
	testSrcBin := b.bin.NewBin(fmt.Sprintf("%s_test_src", audioBinName))
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

	// 20 ms @ 48 kHz
	if err = audioTestSrc.SetProperty("samplesperbuffer", 960); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	audioCaps, err := newAudioCapsFilter(b.conf, livekit.AudioChannel_AUDIO_CHANNEL_BOTH)
	if err != nil {
		return err
	}

	return testSrcBin.AddElements(audioTestSrc, audioCaps)
}

// addAudioFillerSrcBin adds the non-live filler: the source's appsrc on a mixer pad of its own, whose GAP events become
// silence, so the mixer's output reaches the filler's last GAP whether any track's pad is open or not
func (b *AudioBin) addAudioFillerSrcBin() error {
	fillerBin := b.bin.NewBin(fmt.Sprintf("%s_filler_src", audioBinName))
	if err := b.bin.AddSourceBin(fillerBin); err != nil {
		return err
	}

	src := b.conf.AudioFillerSrc
	// an EOS event sent to the appsrc flushes its queue, dropping GAPs still in it; end_of_stream drains it first
	fillerBin.SetEOSFunc(func() bool {
		src.EndStream()
		return false
	})
	src.SetArg("format", "time")
	if err := src.SetProperty("is-live", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	// with caps set, the appsrc sends them and a default segment ahead of its first GAP
	caps, err := newAudioCaps(b.conf, livekit.AudioChannel_AUDIO_CHANNEL_BOTH)
	if err != nil {
		return err
	}
	if err = src.SetProperty("caps", caps); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	srcPad := src.GetStaticPad("src")
	if srcPad == nil {
		return errors.New("audio filler appsrc has no src pad")
	}
	installFillerSilence(srcPad)

	return fillerBin.AddElement(src.Element)
}

// installFillerSilence turns the filler's GAP events into silence in the format of the pad's last caps: the mixer
// drops a GAP buffer whole after mixing a single block of it, then waits on the pad for more data, so its output
// would trail the filler's cover. A GAP with no raw audio caps to size it passes through unchanged.
func installFillerSilence(srcPad *gst.Pad) {
	const maxChunk = 100 * time.Millisecond
	// set from each CAPS event; the appsrc's streaming thread sends both the caps and the GAPs
	var (
		rate, channels int
		silence        []byte
		warned         bool
	)
	srcPad.AddProbe(gst.PadProbeTypeEventDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		event := info.GetEvent()
		if event == nil {
			return gst.PadProbeOK
		}
		switch event.Type() {
		case gst.EventTypeCaps:
			rate, channels, silence = 0, 0, nil
			caps := event.ParseCaps()
			if audioInfo, ok := audio.InfoFromCaps(caps); ok {
				rate, channels, silence = audioInfo.Rate(), audioInfo.Channels(), audioInfo.FormatInfo().Silence()
			} else {
				logger.Warnw("audio filler caps are not raw audio", nil, "caps", caps.String())
			}
			return gst.PadProbeOK
		case gst.EventTypeGap:
		default:
			return gst.PadProbeOK
		}
		if rate <= 0 || channels <= 0 || len(silence) == 0 {
			if !warned {
				warned = true
				logger.Warnw("audio filler gap without raw audio caps, forwarding gaps", nil)
			}
			return gst.PadProbeOK
		}

		start, duration := event.ParseGap()
		end := start + duration
		// samples are counted from the timeline's origin so consecutive GAPs join without rounding drift; splitting at
		// whole seconds keeps every product within int64
		toSample := func(pts time.Duration) int64 {
			return int64(pts/time.Second)*int64(rate) + int64(pts%time.Second)*int64(rate)/int64(time.Second)
		}
		toPTS := func(sample int64) time.Duration {
			return time.Duration(sample/int64(rate))*time.Second +
				time.Duration(sample%int64(rate))*time.Second/time.Duration(rate)
		}
		for from := start; from < end; {
			to := min(from+maxChunk, end)
			samples := toSample(to) - toSample(from)
			if samples > 0 {
				buf := gst.NewBufferFromBytes(bytes.Repeat(silence, int(samples)*channels))
				buf.SetPresentationTimestamp(gst.ClockTime(toPTS(toSample(from))))
				buf.SetDuration(gst.ClockTime(toPTS(toSample(to)) - toPTS(toSample(from))))
				if flow := pad.Push(buf); flow != gst.FlowOK {
					logger.Debugw("audio filler silence refused", "flow", flow, "pts", from)
					return gst.PadProbeDrop
				}
			}
			from = to
		}
		return gst.PadProbeDrop
	})
}

func (b *AudioBin) addMixer() error {
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

	mixedCaps, err := newAudioCapsFilter(b.conf, livekit.AudioChannel_AUDIO_CHANNEL_BOTH)
	if err != nil {
		return err
	}

	subscribeForQoS(audioMixer)

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
		faac, err := newAACEncoder(b.conf.AudioBitrate)
		if err != nil {
			return err
		}
		return b.bin.AddElement(faac)

	case types.MimeTypeMP3:
		mp3enc, err := gst.NewElement("lamemp3enc")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		// target=bitrate is required for cbr and bitrate to take effect;
		// without it lamemp3enc defaults to quality-based VBR.
		mp3enc.SetArg("target", "bitrate")
		if err = mp3enc.SetProperty("cbr", true); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = mp3enc.SetProperty("bitrate", int(b.conf.AudioBitrate)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		return b.bin.AddElement(mp3enc)

	case types.MimeTypeRawAudio:
		return nil

	default:
		return errors.ErrNotSupported(string(b.conf.AudioOutCodec))
	}
}

func newAACEncoder(bitrateKbps int32) (*gst.Element, error) {
	faac, err := gst.NewElement("faac")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	faac.SetArg("rate-control", "ABR")
	if err = faac.SetProperty("bitrate", int(bitrateKbps*1000)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	return faac, nil
}

func addAudioConverter(b *gstreamer.Bin, p *config.PipelineConfig, channel livekit.AudioChannel, isLeaky bool) error {
	audioQueue, err := gstreamer.BuildQueue(fmt.Sprintf("%s_input_queue", audioBinName), p.Latency.PipelineLatency, isLeaky)
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

	rate, err := gstreamer.BuildAudioRate("audio_rate", audioRateTolerance)
	if err != nil {
		return err
	}

	return b.AddElements(audioQueue, audioConvert, audioResample, capsFilter, rate)
}

func installPitchProbes(pacer *audioPacer) {
	// Sink pad: credit the stretch applied to each input buffer.
	if sinkPad := pacer.pitch.GetStaticPad("sink"); sinkPad != nil {
		sinkPad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			if buf := info.GetBuffer(); buf != nil && buf.Duration() != gst.ClockTimeNone {
				pacer.onInput(*buf.Duration().AsDuration())
			}
			return gst.PadProbeOK
		})

		// A FlushStart upstream of pitch (e.g., the discontinuity flush in
		// appwriter.shouldHandleDiscontinuity) causes pitch to drop buffered
		// audio, so part of the credited stretch never reaches the mixer.
		// Cancel any in-flight correction so the next SR re-arms from the
		// post-flush state. Flush events reach only probes that set the flush
		// flag; EventDownstream alone does not include them.
		sinkPad.AddProbe(gst.PadProbeTypeEventDownstream|gst.PadProbeTypeEventFlush, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			event := info.GetEvent()
			if event == nil || event.Type() != gst.EventTypeFlushStart {
				return gst.PadProbeOK
			}
			if !pacer.cancelOnFlush() {
				return gst.PadProbeOK
			}
			if pacer.tc != nil {
				pacer.tc.CancelInFlight()
			}
			logger.Debugw("audio pacer canceled due to upstream flush")
			return gst.PadProbeOK
		})
	}

	if srcPad := pacer.pitch.GetStaticPad("src"); srcPad != nil {
		// Normalize pitch element latency query responses: min latency can go
		// negative, which breaks downstream latency calculations.
		srcPad.AddProbe(gst.PadProbeTypeQueryUpstream|gst.PadProbeTypePull,
			func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
				q := info.GetQuery()
				if q == nil || q.Type() != gst.QueryLatency {
					return gst.PadProbeOK
				}

				live, minimum, maximum := q.ParseLatency()
				if minimum > maximum {
					logger.Debugw("normalizing min latency to 0", "min", minimum)
					minimum = 0
				}
				q.SetLatency(live, minimum, maximum)
				return gst.PadProbeOK
			},
		)
	}
}

// installGapFill turns GAP events at pitch's sink pad into silence: pitch advances its
// timeline only by the samples it receives. The silence goes in buffers the size of a
// packet so a correction in progress completes on it as precisely as on speech. A GAP
// that cannot be converted, before the pad has caps or when pitch refuses the silence,
// passes through unchanged so the mixer still learns about the pause.
func installGapFill(sinkPad *gst.Pad, rate, channels int) {
	const maxChunk = 20 * time.Millisecond
	var padHasGapSegment atomic.Bool
	sinkPad.AddProbe(gst.PadProbeTypeEventDownstream|gst.PadProbeTypeEventFlush, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		event := info.GetEvent()
		if event == nil {
			return gst.PadProbeOK
		}
		switch event.Type() {
		case gst.EventTypeFlushStop:
			padHasGapSegment.Store(false)
			return gst.PadProbeOK
		case gst.EventTypeSegment:
			received := event.ParseSegment()
			current := pad.GetStickyEvent(gst.EventTypeSegment, 0)
			same := current != nil && sameSegment(current.ParseSegment(), received)
			if padHasGapSegment.Swap(false) && !same {
				logger.Warnw("segment differs from the one sent for an audio gap", nil,
					"start", received.GetStart(), "base", received.GetBase(), "time", received.GetTime(), "rate", received.GetRate())
			}
			// pitch discards the audio it holds on every segment, so one that changes nothing is dropped
			if same {
				return gst.PadProbeDrop
			}
			return gst.PadProbeOK
		case gst.EventTypeGap:
		default:
			return gst.PadProbeOK
		}
		if pad.GetCurrentCaps() == nil {
			return gst.PadProbeOK
		}
		// a track's first GAP precedes any segment: the RTP depayloader sends its own, a default TIME one, with its first packet
		if pad.GetStickyEvent(gst.EventTypeSegment, 0) == nil {
			segment := gst.NewFormattedSegment(gst.FormatTime)
			sent := pad.SendEvent(gst.NewSegmentEvent(segment))
			segment.Free()
			if !sent {
				logger.Warnw("could not send pitch a segment for an audio gap, forwarding the gap", nil)
				return gst.PadProbeOK
			}
			padHasGapSegment.Store(true)
		}
		start, remaining := event.ParseGap()
		for remaining > 0 {
			chunk := min(remaining, maxChunk)
			samples := int64(chunk) * int64(rate) / int64(time.Second)
			if samples <= 0 {
				break
			}
			buf := gst.NewBufferFromBytes(make([]byte, samples*int64(channels)*4))
			buf.SetPresentationTimestamp(gst.ClockTime(start))
			buf.SetDuration(gst.ClockTime(chunk))
			buf.SetFlags(gst.BufferFlagGap)
			// recursive stream lock: the silence enters pitch in order
			if flow := pad.Chain(buf); flow != gst.FlowOK {
				logger.Debugw("could not fill audio gap before pitch, forwarding the gap", "flow", flow, "filled", start)
				return gst.PadProbeOK
			}
			start += chunk
			remaining -= chunk
		}
		return gst.PadProbeDrop
	})
}

// sameSegment reports whether two segments differ at most in position, which only records progress
func sameSegment(a, b *gst.Segment) bool {
	return a.GetFormat() == b.GetFormat() && a.GetFlags() == b.GetFlags() &&
		a.GetRate() == b.GetRate() && a.GetAppliedRate() == b.GetAppliedRate() &&
		a.GetBase() == b.GetBase() && a.GetOffset() == b.GetOffset() &&
		a.GetStart() == b.GetStart() && a.GetStop() == b.GetStop() &&
		a.GetTime() == b.GetTime() && a.GetDuration() == b.GetDuration()
}

// audioFloatFormat is the sample rate and channel count of the F32 audio around pitch
func audioFloatFormat(p *config.PipelineConfig, channel livekit.AudioChannel) (rate, channels int) {
	rate = 48000
	if p.AudioOutCodec == types.MimeTypeAAC || p.AudioOutCodec == types.MimeTypeMP3 {
		rate = int(p.AudioFrequency)
	}
	channels = 1
	if channel == livekit.AudioChannel_AUDIO_CHANNEL_BOTH {
		channels = 2
	}
	return rate, channels
}

func (b *AudioBin) addAudioConvertWithPitch(bin *gstreamer.Bin, p *config.PipelineConfig, channel livekit.AudioChannel, isLeaky bool) (*audioPacer, error) {
	q, err := gstreamer.BuildQueue(fmt.Sprintf("%s_input_queue", audioBinName), p.Latency.PipelineLatency, isLeaky)
	if err != nil {
		return nil, err
	}

	ac1, err := gst.NewElement("audioconvert")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	ar1, err := gst.NewElement("audioresample")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	// go to float for pitch element
	f32caps, err := newAudioFloatCapsFilter(p, channel)
	if err != nil {
		return nil, err
	}

	// no audiorate here: it would delete or insert samples before pitch could resample them
	pitch, err := gst.NewElement("pitch")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	pitch.SetArg("tempo", fmt.Sprintf("%.1f", 1.0))

	ac2, err := gst.NewElement("audioconvert")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	// back to pipeline/native format
	s16caps, err := newAudioCapsFilter(p, channel)
	if err != nil {
		return nil, err
	}

	// Boosted rate kicks in when the controller signals soft-budget exceeded.
	// 2x the base rate, capped at the same 0.2 ceiling enforced for AdjustmentRate.
	boostedRate := 2 * p.AudioTempoController.AdjustmentRate
	if boostedRate > 0.2 {
		boostedRate = 0.2
	}

	pacer := newAudioPacer(pitch, p.AudioTempoController.AdjustmentRate, boostedRate)

	installPitchProbes(pacer)
	if sinkPad := pitch.GetStaticPad("sink"); sinkPad != nil {
		rate, channels := audioFloatFormat(p, channel)
		installGapFill(sinkPad, rate, channels)
	}

	if err := bin.AddElements(q, ac1, ar1, f32caps, pitch, ac2, s16caps); err != nil {
		return nil, err
	}
	return pacer, nil
}

// F32 caps used only around `pitch`
func newAudioFloatCapsFilter(p *config.PipelineConfig, channel livekit.AudioChannel) (*gst.Element, error) {
	var channelCaps string
	if channel == livekit.AudioChannel_AUDIO_CHANNEL_BOTH {
		channelCaps = "channels=2"
	} else {
		channelCaps = fmt.Sprintf("channels=1,channel-mask=(bitmask)0x%d", channel)
	}
	rate, _ := audioFloatFormat(p, channel)
	caps := gst.NewCapsFromString(fmt.Sprintf("audio/x-raw,format=F32LE,layout=interleaved,rate=%d,%s", rate, channelCaps))

	cf, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = cf.SetProperty("caps", caps); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	return cf, nil
}

func newAudioCapsFilter(p *config.PipelineConfig, channel livekit.AudioChannel) (*gst.Element, error) {
	caps, err := newAudioCaps(p, channel)
	if err != nil {
		return nil, err
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

// newAudioCaps is the raw S16 format the mixer runs in, for the given channel
func newAudioCaps(p *config.PipelineConfig, channel livekit.AudioChannel) (*gst.Caps, error) {
	var channelCaps string
	if channel == livekit.AudioChannel_AUDIO_CHANNEL_BOTH {
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
	case types.MimeTypeAAC, types.MimeTypeMP3:
		caps = gst.NewCapsFromString(fmt.Sprintf(
			"audio/x-raw,format=S16LE,layout=interleaved,rate=%d,%s",
			p.AudioFrequency, channelCaps,
		))
	default:
		return nil, errors.ErrNotSupported(string(p.AudioOutCodec))
	}

	return caps, nil
}

func subscribeForQoS(mixer *gst.Element) {
	mixer.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if err := pad.SetProperty("qos-messages", true); err != nil {
			logger.Errorw("failed to set QoS messages on pad", err)
		}
	})
}
