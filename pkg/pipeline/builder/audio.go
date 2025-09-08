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

	leakyQueue    = true
	blockingQueue = false

	opusPlcMaxFrames          = 5
	opusDecStatsPollInterval  = time.Second * 5
	opusDecPlcMaxJitter       = 3 * time.Millisecond
	defaultOpusPacketDuration = time.Millisecond * 20
)

type AudioBin struct {
	bin  *gstreamer.Bin
	conf *config.PipelineConfig

	mu          sync.Mutex
	nextID      int
	nextChannel int
	names       map[string]string
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
		queue, err := gstreamer.BuildQueue("audio_queue", p.Latency.PipelineLatency, leakyQueue)
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

		opusParse, err := gst.NewElement("opusparse")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		opusDec, err := gst.NewElement("opusdec")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		err = opusDec.SetProperty("plc", true)
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		if err = appSrcBin.AddElements(rtpOpusDepay, opusParse, opusDec); err != nil {
			return err
		}
		installOpusParseSrcProbe(opusParse, opusDec)

	default:
		return errors.ErrNotSupported(string(ts.MimeType))
	}

	if err := addAudioConverter(appSrcBin, b.conf, b.getChannel(ts), blockingQueue); err != nil {
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

	case types.MimeTypeRawAudio:
		return nil

	default:
		return errors.ErrNotSupported(string(b.conf.AudioOutCodec))
	}
}

func addAudioConverter(b *gstreamer.Bin, p *config.PipelineConfig, channel int, isLeaky bool) error {
	audioQueue, err := gstreamer.BuildQueue("audio_input_queue", p.Latency.PipelineLatency, isLeaky)
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

func subscribeForQoS(mixer *gst.Element) {
	mixer.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if err := pad.SetProperty("qos-messages", true); err != nil {
			logger.Errorw("failed to set QoS messages on pad", err)
		}
	})
}

func installOpusParseSrcProbe(opusParse *gst.Element, opusDec *gst.Element) {
	src := opusParse.GetStaticPad("src")

	var lastPTS, lastDur time.Duration
	var lastPoll time.Time

	src.AddProbe(gst.PadProbeTypeBuffer, func(p *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		if buf == nil {
			return gst.PadProbeOK
		}

		pts := time.Duration(buf.PresentationTimestamp())
		dur := time.Duration(buf.Duration())

		if dur <= 0 {
			// Fallback if TOC wasn’t parsed (shouldn’t happen with opusparse)
			if lastDur > 0 {
				dur = lastDur
			} else {
				dur = defaultOpusPacketDuration
			}
		}

		if lastDur > 0 {
			expected := lastPTS + lastDur
			if pts > expected {
				gap := pts - expected
				// Only trigger for at least ~one full frame gap
				if gap+opusDecPlcMaxJitter >= lastDur {
					// k missing frames (rounded)
					k := int((gap + lastDur - 1) / lastDur)
					if k < 1 {
						k = 1
					}
					if k <= opusPlcMaxFrames {
						missed := time.Duration(k) * lastDur
						// Push GAP so opusdec generates PLC
						gapEv := gst.NewGapEvent(gst.ClockTime(expected), gst.ClockTime(missed))
						p.PushEvent(gapEv)
						buf.SetFlags(buf.GetFlags() | gst.BufferFlagDiscont)
					}
				}
			}
		}
		lastPTS, lastDur = pts, dur

		// periodically gather stats from opusdec
		if lastPoll.IsZero() || time.Since(lastPoll) >= opusDecStatsPollInterval {
			stats, err := getOpusDecStats(opusDec)
			if err != nil {
				logger.Debugw("opusdec stats: parse error", "err", err)
				return gst.PadProbeOK
			}
			postOpusDecStatsMessage(opusDec, stats)
			lastPoll = time.Now()
		}

		return gst.PadProbeOK

	})
}
