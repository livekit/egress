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
	"go.uber.org/atomic"

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
	nextChannel int
	names       map[string]string

	audioPacer *audioPacer
}

type driftProcessNotifier interface {
	DriftProcessed()
}

type audioPacer struct {
	pitch               *gst.Element
	active              atomic.Bool
	remaining           time.Duration
	tc                  driftProcessNotifier
	tempoAdjustmentRate float64
}

func (a *audioPacer) start(drift time.Duration) {
	if a.pitch == nil || drift == 0 {
		return
	}
	if a.active.Load() {
		logger.Errorw(
			"starting audio pacer, but it's already active",
			errors.New("tempo controller bug"),
		)
		return
	}

	rate := 1 + a.tempoAdjustmentRate
	if drift > 0 {
		rate = 1 - a.tempoAdjustmentRate
	}
	compensationFactor := 1 / a.tempoAdjustmentRate
	driftNanoseconds := int64(drift)
	compensationNanoseconds := int64(compensationFactor * float64(driftNanoseconds))
	compensationDuration := time.Duration(compensationNanoseconds)

	a.remaining = compensationDuration.Abs()
	logger.Debugw("starting audio pacer", "remaining", a.remaining, "rate", rate)
	a.pitch.SetArg("tempo", fmt.Sprintf("%.2f", rate))
	a.active.Store(true)

}

func (a *audioPacer) observeProcessedDuration(d time.Duration) {
	if !a.active.Load() {
		return
	}
	a.remaining -= d
	if a.remaining <= 0 {
		logger.Debugw("audio gap processed, stopping the pacer")
		a.stop()
		a.tc.DriftProcessed()
	}
}

func (a *audioPacer) stop() {
	if a.pitch == nil || a.tc == nil {
		return
	}
	a.pitch.SetArg("tempo", fmt.Sprintf("%.1f", 1.0))
	a.active.Store(false)
	a.remaining = 0
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
		queue, err := gstreamer.BuildQueue(fmt.Sprintf("%s_queue", audioBinName), p.Latency.PipelineLatency, leakyQueue)
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

	if err = addAudioConverter(b.bin, b.conf, audioChannelStereo, leakyQueue); err != nil {
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

	case types.MimeTypePCMU:
		if err := ts.AppSrc.Element.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
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
		if err := ts.AppSrc.Element.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
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

	addAudioConvertFunc := addAudioConverter
	if b.conf.AudioTempoController.Enabled {
		addAudioConvertFunc = b.addAudioConvertWithPitch
	}

	if err := addAudioConvertFunc(appSrcBin, b.conf, b.getChannel(ts), blockingQueue); err != nil {
		return err
	}

	if err := b.bin.AddSourceBin(appSrcBin); err != nil {
		return err
	}

	if ts.TempoController != nil {
		ts.TempoController.OnDriftDetectedCallback(func(drift time.Duration) {
			if b.audioPacer.pitch != nil {
				logger.Debugw("starting audio pacer to cover the drift", "drift", drift)
				b.audioPacer.start(drift)
			}
		})
		b.audioPacer.tc = ts.TempoController
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
		}
		return audioChannelRight

	case livekit.AudioMixing_DUAL_CHANNEL_ALTERNATE:
		next := b.nextChannel
		b.nextChannel++
		return next%2 + 1
	}

	return audioChannelStereo
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

	audioCaps, err := newAudioCapsFilter(b.conf, audioChannelStereo)
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

	mixedCaps, err := newAudioCapsFilter(b.conf, audioChannelStereo)
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
		if err = mp3enc.SetProperty("bitrate", int(b.conf.AudioBitrate)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = mp3enc.SetProperty("cbr", true); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		return b.bin.AddElement(mp3enc)

	case types.MimeTypeRawAudio:
		return nil

	default:
		return errors.ErrNotSupported(string(b.conf.AudioOutCodec))
	}
}

func addAudioConverter(b *gstreamer.Bin, p *config.PipelineConfig, channel int, isLeaky bool) error {
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

func (b *AudioBin) installPitchProbes() {
	if b.audioPacer.pitch == nil {
		return
	}
	if sinkPad := b.audioPacer.pitch.GetStaticPad("sink"); sinkPad != nil {
		sinkPad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			if !b.audioPacer.active.Load() {
				return gst.PadProbeOK
			}
			if buf := info.GetBuffer(); buf != nil && buf.Duration() != gst.ClockTimeNone {
				b.audioPacer.observeProcessedDuration(*buf.Duration().AsDuration())
			}
			return gst.PadProbeOK
		})
	}
	if srcPad := b.audioPacer.pitch.GetStaticPad("src"); srcPad != nil {
		// pitch element min latency can go negative, so we need to normalize it
		// to workaround the obvious issue with the element latency query handling
		srcPad.AddProbe(gst.PadProbeTypeQueryUpstream|gst.PadProbeTypePull,
			func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
				q := info.GetQuery()
				if q == nil || q.Type() != gst.QueryLatency {
					return gst.PadProbeOK
				}

				live, min, max := q.ParseLatency()
				// Normalize: ensure min <= max
				if min > max {
					logger.Debugw("normalizing min latency to 0", "min", min)
					min = 0
				}
				q.SetLatency(live, min, max)
				return gst.PadProbeOK
			},
		)
	}
}

func (b *AudioBin) addAudioConvertWithPitch(bin *gstreamer.Bin, p *config.PipelineConfig, channel int, isLeaky bool) error {
	// add audio rate element to handle discontinuities or codec DTX
	rate, err := gstreamer.BuildAudioRate("audio_rate", audioRateTolerance)
	if err != nil {
		return err
	}

	q, err := gstreamer.BuildQueue(fmt.Sprintf("%s_input_queue", audioBinName), p.Latency.PipelineLatency, isLeaky)
	if err != nil {
		return err
	}

	ac1, err := gst.NewElement("audioconvert")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	ar1, err := gst.NewElement("audioresample")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	// go to float for pitch element
	f32caps, err := newAudioFloatCapsFilter(p, channel)
	if err != nil {
		return err
	}

	pitch, err := gst.NewElement("pitch")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	pitch.SetArg("tempo", fmt.Sprintf("%.1f", 1.0))

	ac2, err := gst.NewElement("audioconvert")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	// back to pipeline/native format
	s16caps, err := newAudioCapsFilter(p, channel)
	if err != nil {
		return err
	}

	// keep a handle for pacer control
	b.audioPacer = &audioPacer{
		pitch:               pitch,
		tempoAdjustmentRate: p.AudioTempoController.AdjustmentRate,
	}

	b.installPitchProbes()

	return bin.AddElements(rate, q, ac1, ar1, f32caps, pitch, ac2, s16caps)
}

// F32 caps used only around `pitch`
func newAudioFloatCapsFilter(p *config.PipelineConfig, channel int) (*gst.Element, error) {
	var channelCaps string
	if channel == audioChannelStereo {
		channelCaps = "channels=2"
	} else {
		channelCaps = fmt.Sprintf("channels=1,channel-mask=(bitmask)0x%d", channel)
	}
	rate := 48000
	if p.AudioOutCodec == types.MimeTypeAAC {
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
	case types.MimeTypeMP3:
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
