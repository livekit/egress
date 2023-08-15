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

package input

import (
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/types"
)

const audioMixerLatency = uint64(2e9)

type audioInput struct {
	src     []*gst.Element
	testSrc []*gst.Element
	mixer   []*gst.Element
	encoder *gst.Element
}

func (a *audioInput) buildWebInput(p *config.PipelineConfig) error {
	pulseSrc, err := gst.NewElement("pulsesrc")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = pulseSrc.SetProperty("device", fmt.Sprintf("%s.monitor", p.Info.EgressId)); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	a.src = []*gst.Element{pulseSrc}
	return a.buildConverter(p)
}

func (a *audioInput) buildSDKInput(p *config.PipelineConfig) error {
	if p.AudioTrack != nil {
		if err := a.buildAppSource(p.AudioTrack); err != nil {
			return err
		}
		if err := a.buildConverter(p); err != nil {
			return err
		}
	}

	if err := a.buildTestSrc(p); err != nil {
		return err
	}

	return a.buildMixer(p)
}

func (a *audioInput) buildAppSource(track *config.TrackSource) error {
	track.AppSrc.Element.SetArg("format", "time")
	if err := track.AppSrc.Element.SetProperty("is-live", true); err != nil {
		return err
	}
	a.src = []*gst.Element{track.AppSrc.Element}

	switch track.MimeType {
	case types.MimeTypeOpus:
		if err := track.AppSrc.Element.SetProperty("caps", gst.NewCapsFromString(fmt.Sprintf(
			"application/x-rtp,media=audio,payload=%d,encoding-name=OPUS,clock-rate=%d",
			track.PayloadType, track.ClockRate,
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

		a.src = append(a.src, rtpOpusDepay, opusDec)

	default:
		return errors.ErrNotSupported(string(track.MimeType))
	}

	return nil
}

func (a *audioInput) buildConverter(p *config.PipelineConfig) error {
	audioQueue, err := builder.BuildQueue("audio_input_queue", p.Latency, true)
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

	a.src = append(a.src, audioQueue, audioConvert, audioResample, capsFilter)
	return nil
}

func (a *audioInput) buildTestSrc(p *config.PipelineConfig) error {
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

	audioCaps, err := newAudioCapsFilter(p)
	if err != nil {
		return err
	}

	a.testSrc = []*gst.Element{audioTestSrc, audioCaps}
	return nil
}

func (a *audioInput) buildMixer(p *config.PipelineConfig) error {
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

	a.mixer = []*gst.Element{audioMixer, mixedCaps}
	return nil
}

func (a *audioInput) buildEncoder(p *config.PipelineConfig) error {
	switch p.AudioOutCodec {
	case types.MimeTypeOpus:
		encoder, err := gst.NewElement("opusenc")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = encoder.SetProperty("bitrate", int(p.AudioBitrate*1000)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		a.encoder = encoder

	case types.MimeTypeAAC:
		encoder, err := gst.NewElement("faac")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = encoder.SetProperty("bitrate", int(p.AudioBitrate*1000)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		a.encoder = encoder

	case types.MimeTypeRawAudio:
		return nil

	default:
		return errors.ErrNotSupported(string(p.AudioOutCodec))
	}

	return nil
}

func newAudioCapsFilter(p *config.PipelineConfig) (*gst.Element, error) {
	var caps *gst.Caps
	switch p.AudioOutCodec {
	case types.MimeTypeOpus, types.MimeTypeRawAudio:
		caps = gst.NewCapsFromString(
			"audio/x-raw,format=S16LE,layout=interleaved,rate=48000,channels=2",
		)
	case types.MimeTypeAAC:
		caps = gst.NewCapsFromString(fmt.Sprintf("audio/x-raw,format=S16LE,layout=interleaved,rate=%d,channels=2",
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

func (a *audioInput) link() (*gst.GhostPad, error) {
	if a.src != nil {
		// link src elements
		if err := gst.ElementLinkMany(a.src...); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}

	if a.testSrc != nil {
		// link test src elements
		if err := gst.ElementLinkMany(a.testSrc...); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}

	if a.mixer != nil {
		if a.src != nil {
			// link src to mixer
			if err := builder.LinkPads(
				"audio src", builder.GetSrcPad(a.src),
				"audio mixer", a.mixer[0].GetRequestPad("sink_%u"),
			); err != nil {
				return nil, err
			}
		}

		if a.testSrc != nil {
			// link test src to mixer
			if err := builder.LinkPads(
				"audio test src", builder.GetSrcPad(a.testSrc),
				"audio mixer", a.mixer[0].GetRequestPad("sink_%u"),
			); err != nil {
				return nil, err
			}
		}

		if err := gst.ElementLinkMany(a.mixer...); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}

	var ghostPad *gst.Pad
	if a.encoder != nil {
		if a.mixer != nil {
			// link mixer to encoder
			if err := builder.LinkPads(
				"audio mixer", builder.GetSrcPad(a.mixer),
				"audio encoder", a.encoder.GetStaticPad("sink"),
			); err != nil {
				return nil, err
			}
		} else {
			// link src to encoder
			if err := builder.LinkPads(
				"audio src", builder.GetSrcPad(a.src),
				"audio encoder", a.encoder.GetStaticPad("sink"),
			); err != nil {
				return nil, err
			}
		}
		ghostPad = a.encoder.GetStaticPad("src")
	} else if a.mixer != nil {
		ghostPad = builder.GetSrcPad(a.mixer)
	} else {
		ghostPad = builder.GetSrcPad(a.src)
	}

	return gst.NewGhostPad("audio_src", ghostPad), nil
}
