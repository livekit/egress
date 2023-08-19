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

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
)

const audioMixerLatency = uint64(2e9)

func BuildAudioBin(pipeline *gstreamer.Pipeline, p *config.PipelineConfig) (*gstreamer.Bin, error) {
	b := pipeline.NewBin("audio", gstreamer.BinTypeAudio)

	switch p.SourceType {
	case types.SourceTypeSDK:
		if err := buildSDKAudioInput(pipeline, b, p); err != nil {
			return nil, err
		}

	case types.SourceTypeWeb:
		if err := buildWebAudioInput(b, p); err != nil {
			return nil, err
		}
	}

	if len(p.Outputs) > 1 {
		tee, err := gst.NewElementWithName("tee", "audio_tee")
		if err != nil {
			return nil, err
		}

		if err = b.AddElement(tee); err != nil {
			return nil, err
		}
	}

	return b, nil
}

func buildWebAudioInput(b *gstreamer.Bin, p *config.PipelineConfig) error {
	pulseSrc, err := gst.NewElement("pulsesrc")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = pulseSrc.SetProperty("device", fmt.Sprintf("%s.monitor", p.Info.EgressId)); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if err = b.AddElement(pulseSrc); err != nil {
		return err
	}

	if err = addAudioConverter(b, p); err != nil {
		return err
	}

	if p.AudioTranscoding {
		if err = addAudioEncoder(b, p); err != nil {
			return err
		}
	}

	return nil
}

func buildSDKAudioInput(pipeline *gstreamer.Pipeline, b *gstreamer.Bin, p *config.PipelineConfig) error {
	if p.AudioTrack != nil {
		appSrcBin, err := buildAudioAppSrcBin(pipeline, p)
		if err != nil {
			return err
		}
		if err = b.AddSourceBin(appSrcBin); err != nil {
			return err
		}
	}

	testSrcBin, err := buildAudioTestSrcBin(pipeline, p)
	if err != nil {
		return err
	}
	if err = b.AddSourceBin(testSrcBin); err != nil {
		return err
	}

	if err = addAudioMixer(b, p); err != nil {
		return err
	}
	if p.AudioTranscoding {
		if err = addAudioEncoder(b, p); err != nil {
			return err
		}
	}

	return nil
}

func buildAudioAppSrcBin(pipeline *gstreamer.Pipeline, p *config.PipelineConfig) (*gstreamer.Bin, error) {
	track := p.AudioTrack

	b := pipeline.NewBin(track.TrackID, gstreamer.BinTypeAudio)
	b.SetEOSFunc(track.EOSFunc)

	track.AppSrc.Element.SetArg("format", "time")
	if err := track.AppSrc.Element.SetProperty("is-live", true); err != nil {
		return nil, err
	}

	if err := b.AddElement(track.AppSrc.Element); err != nil {
		return nil, err
	}

	switch track.MimeType {
	case types.MimeTypeOpus:
		if err := track.AppSrc.Element.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=audio,payload=%d,encoding-name=OPUS,clock-rate=%d",
			track.PayloadType, track.ClockRate,
		))); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		rtpOpusDepay, err := gst.NewElement("rtpopusdepay")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		opusDec, err := gst.NewElement("opusdec")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		if err = b.AddElements(rtpOpusDepay, opusDec); err != nil {
			return nil, err
		}

	default:
		return nil, errors.ErrNotSupported(string(track.MimeType))
	}

	if err := addAudioConverter(b, p); err != nil {
		return nil, err
	}

	return b, nil
}

func buildAudioTestSrcBin(pipeline *gstreamer.Pipeline, p *config.PipelineConfig) (*gstreamer.Bin, error) {
	b := pipeline.NewBin("audio_test_src", gstreamer.BinTypeAudio)

	audioTestSrc, err := gst.NewElement("audiotestsrc")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = audioTestSrc.SetProperty("volume", 0.0); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = audioTestSrc.SetProperty("do-timestamp", true); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = audioTestSrc.SetProperty("is-live", true); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	audioCaps, err := newAudioCapsFilter(p)
	if err != nil {
		return nil, err
	}

	if err = b.AddElements(audioTestSrc, audioCaps); err != nil {
		return nil, err
	}

	return b, nil
}

func addAudioConverter(b *gstreamer.Bin, p *config.PipelineConfig) error {
	audioQueue, err := gstreamer.BuildQueue("audio_input_queue", p.Latency, true)
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

	capsFilter, err := newAudioCapsFilter(p)
	if err != nil {
		return err
	}

	return b.AddElements(audioQueue, audioConvert, audioResample, capsFilter)
}

func addAudioMixer(b *gstreamer.Bin, p *config.PipelineConfig) error {
	audioMixer, err := gst.NewElement("audiomixer")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = audioMixer.SetProperty("latency", audioMixerLatency); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	mixedCaps, err := newAudioCapsFilter(p)
	if err != nil {
		return err
	}

	return b.AddElements(audioMixer, mixedCaps)
}

func addAudioEncoder(b *gstreamer.Bin, p *config.PipelineConfig) error {
	switch p.AudioOutCodec {
	case types.MimeTypeOpus:
		opusEnc, err := gst.NewElement("opusenc")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = opusEnc.SetProperty("bitrate", int(p.AudioBitrate*1000)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		return b.AddElement(opusEnc)

	case types.MimeTypeAAC:
		faac, err := gst.NewElement("faac")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = faac.SetProperty("bitrate", int(p.AudioBitrate*1000)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		return b.AddElement(faac)

	case types.MimeTypeRawAudio:
		return nil

	default:
		return errors.ErrNotSupported(string(p.AudioOutCodec))
	}
}

func newAudioCapsFilter(p *config.PipelineConfig) (*gst.Element, error) {
	var caps *gst.Caps
	switch p.AudioOutCodec {
	case types.MimeTypeOpus, types.MimeTypeRawAudio:
		caps = gst.NewCapsFromString(
			"audio/x-raw,format=S16LE,layout=interleaved,rate=48000,channels=2",
		)
	case types.MimeTypeAAC:
		caps = gst.NewCapsFromString(fmt.Sprintf(
			"audio/x-raw,format=S16LE,layout=interleaved,rate=%d,channels=2",
			p.AudioFrequency,
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
