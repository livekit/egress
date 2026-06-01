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
	"github.com/go-gst/go-gst/gst/app"
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
		pipeline.AddOnSourceBinReset(b.onSourceBinReset)
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
		queue, err := gstreamer.BuildQueue(fmt.Sprintf("%s_queue", audioBinName), p.Latency.PipelineLatency, p.Live)
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
		ts.TempoController.OnDriftDetectedCallback(func(drift time.Duration) {
			if pacer.pitch != nil {
				logger.Debugw("starting audio pacer to cover the drift", "drift", drift)
				pacer.start(drift)
			}
		})
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
				logger.Errorw("audio drift exceeded hard budget",
					errors.New("uncorrected drift above hard budget"),
					"trackID", trackID, "tier", tier)
				pacer.boost()
			}
		})
		pacer.tc = ts.TempoController
	}

	return nil
}

func (b *AudioBin) onSourceBinReset(ts *config.TrackSource) error {
	if ts.TrackKind != lksdk.TrackKindAudio {
		return nil
	}
	return b.resetAudioAppSrcBin(ts)
}

func (b *AudioBin) resetAudioAppSrcBin(ts *config.TrackSource) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	oldName, ok := b.names[ts.TrackID]
	if !ok {
		return errors.New("track already removed, cannot reset audio source bin")
	}

	if b.bin.GetState() > gstreamer.StateRunning {
		return errors.New("pipeline stopping, cannot reset audio source bin")
	}

	// Detach the tempo controller callback so a concurrent SetDrift can't invoke
	// the old pacer's closure on the soon-to-be-freed pitch element. The new
	// callback is re-registered inside addAudioAppSrcBinLocked below.
	//
	// CancelInFlight clears the in-flight target so the immediate-callback fire
	// inside the new OnDriftDetectedCallback registration does not arm the new
	// pacer with the old pacer's target. The old pacer's partial compensation
	// is downstream of the bin being discarded — re-applying it on the new
	// pacer would double-correct. The next SR will surface any residual drift
	// and the controller arms fresh against the current state.
	if ts.TempoController != nil {
		ts.TempoController.OnDriftDetectedCallback(nil)
		ts.TempoController.OnTierChange(nil)
		ts.TempoController.CancelInFlight()
	}

	// Force-remove old bin (blocks on GLib main loop, safe to hold b.mu since
	// ForceRemoveSourceBin only acquires gstreamer.Bin's internal mutex)
	if err := b.bin.ForceRemoveSourceBin(oldName); err != nil {
		return fmt.Errorf("failed to force remove audio source bin: %w", err)
	}

	newElement, err := gst.NewElementWithName("appsrc", fmt.Sprintf("app_%s", ts.TrackID))
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	ts.AppSrc = app.SrcFromElement(newElement)

	if err := b.addAudioAppSrcBinLocked(ts); err != nil {
		return fmt.Errorf("failed to add new audio source bin: %w", err)
	}

	logger.Infow("audio source bin reset complete", "trackID", ts.TrackID, "newBin", b.names[ts.TrackID])
	return nil
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
	rate, err := gstreamer.BuildAudioRate("audio_rate", audioRateTolerance)
	if err != nil {
		return err
	}

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

	return b.AddElements(rate, audioQueue, audioConvert, audioResample, capsFilter)
}

func installPitchProbes(pacer *audioPacer) {
	if pacer.pitch == nil {
		return
	}

	// Sink pad: accumulate input buffer durations.
	if sinkPad := pacer.pitch.GetStaticPad("sink"); sinkPad != nil {
		sinkPad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			if buf := info.GetBuffer(); buf != nil && buf.Duration() != gst.ClockTimeNone {
				pacer.inputAccum.Add(int64(*buf.Duration().AsDuration()))
			}
			return gst.PadProbeOK
		})

		// A FlushStart upstream of pitch (e.g., the discontinuity flush in
		// appwriter.shouldHandleDiscontinuity) causes pitch to drop buffered
		// audio. The probe accumulators (inputAccum / outputAccum) keep
		// growing across the flush, but the input/output samples are no
		// longer aligned — outputDelta no longer reflects the actual
		// compensation that survived to the mixer. Cancel any in-flight
		// correction so the next SR re-arms from the post-flush state.
		sinkPad.AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
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
		// Accumulate output buffer durations and check correction completion.
		// Actual compensation = outputDelta - inputDelta (positive when slowing
		// down, negative when speeding up — same sign as the target drift).
		srcPad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			if buf := info.GetBuffer(); buf != nil && buf.Duration() != gst.ClockTimeNone {
				pacer.outputAccum.Add(int64(*buf.Duration().AsDuration()))
			}
			if !pacer.active.Load() {
				return gst.PadProbeOK
			}
			// Snapshot is published before active=true in start(); a non-nil
			// load is guaranteed once active is observed true. The nil guard
			// is defensive against pathological orderings on weak-memory
			// platforms.
			snap := pacer.snapshot.Load()
			if snap == nil {
				return gst.PadProbeOK
			}
			inputDelta := pacer.inputAccum.Load() - snap.inputAtStart
			outputDelta := pacer.outputAccum.Load() - snap.outputAtStart
			compensation := time.Duration(outputDelta - inputDelta)
			if compensation.Abs() >= snap.targetDrift.Abs() {
				// stop() returns false if another probe (or a concurrent flush
				// cancel) already won the transition; in that case, the
				// controller has already been notified — do not double-notify.
				if pacer.stop() {
					logger.Debugw("audio drift corrected", "target", snap.targetDrift, "actual", compensation)
					pacer.tc.DriftProcessed(compensation)
				}
			}
			return gst.PadProbeOK
		})

		// Normalize pitch element latency query responses — min latency can go
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

func (b *AudioBin) addAudioConvertWithPitch(bin *gstreamer.Bin, p *config.PipelineConfig, channel livekit.AudioChannel, isLeaky bool) (*audioPacer, error) {
	// add audio rate element to handle discontinuities or codec DTX
	rate, err := gstreamer.BuildAudioRate("audio_rate", audioRateTolerance)
	if err != nil {
		return nil, err
	}

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

	pacer := &audioPacer{
		pitch:               pitch,
		tempoAdjustmentRate: p.AudioTempoController.AdjustmentRate,
		boostedRate:         boostedRate,
	}

	installPitchProbes(pacer)

	if err := bin.AddElements(rate, q, ac1, ar1, f32caps, pitch, ac2, s16caps); err != nil {
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
	rate := 48000
	if p.AudioOutCodec == types.MimeTypeAAC || p.AudioOutCodec == types.MimeTypeMP3 {
		rate = int(p.AudioFrequency)
	}
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
