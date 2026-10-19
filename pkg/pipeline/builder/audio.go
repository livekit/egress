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
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/linkdata/deadlock"

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
		faac, err := gst.NewElement("faac")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = faac.SetProperty("bitrate", int(b.conf.AudioBitrate*1000)); err != nil {
			return errors.ErrGstPipelineError(err)
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
	sinkPad.AddProbe(gst.PadProbeTypeEventDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		event := info.GetEvent()
		if event == nil || event.Type() != gst.EventTypeGap {
			return gst.PadProbeOK
		}
		if pad.GetCurrentCaps() == nil {
			return gst.PadProbeOK
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

	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = capsFilter.SetProperty("caps", caps); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return capsFilter, nil
}

func subscribeForQoS(mixer *gst.Element) {
	mixer.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if err := pad.SetProperty("qos-messages", true); err != nil {
			logger.Errorw("failed to set QoS messages on pad", err)
		}
	})
}
