package input

import (
	"fmt"
	"strings"

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

func (a *audioInput) buildWebSource(p *config.PipelineConfig) error {
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

func (a *audioInput) buildAppSource(p *config.PipelineConfig) error {
	if p.AudioSrc != nil {
		src := p.AudioSrc
		src.Element.SetArg("format", "time")
		if err := src.Element.SetProperty("is-live", true); err != nil {
			return err
		}
		a.src = []*gst.Element{src.Element}

		switch {
		case strings.EqualFold(p.AudioCodecParams.MimeType, string(types.MimeTypeOpus)):
			if err := src.Element.SetProperty("caps", gst.NewCapsFromString(
				fmt.Sprintf(
					"application/x-rtp,media=audio,payload=%d,encoding-name=OPUS,clock-rate=%d",
					p.AudioCodecParams.PayloadType, p.AudioCodecParams.ClockRate,
				),
			)); err != nil {
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
			return errors.ErrNotSupported(p.AudioCodecParams.MimeType)
		}

		if err := a.buildConverter(p); err != nil {
			return err
		}
	}

	return a.buildMixer(p)
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

func (a *audioInput) buildMixer(p *config.PipelineConfig) error {
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
		caps = gst.NewCapsFromString(
			fmt.Sprintf("audio/x-raw,format=S16LE,layout=interleaved,rate=%d,channels=2", p.AudioFrequency),
		)
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
		if err := gst.ElementLinkMany(a.src...); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}
	if a.mixer != nil {
		if err := builder.LinkPads("audio decoder", builder.GetSrcPad(a.src), "audio mixer", a.mixer[0].GetRequestPad("sink_%u")); err != nil {
			return nil, err
		}
		if err := gst.ElementLinkMany(a.testSrc...); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err := builder.LinkPads("audio test src", builder.GetSrcPad(a.testSrc), "audio mixer", a.mixer[0].GetRequestPad("sink_%u")); err != nil {
			return nil, err
		}
		if err := gst.ElementLinkMany(a.mixer...); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
	}

	var srcPad *gst.Pad
	if a.encoder != nil {
		if a.mixer != nil {
			if err := builder.LinkPads("audio mixer", builder.GetSrcPad(a.mixer), "audio encoder", a.encoder.GetStaticPad("sink")); err != nil {
				return nil, err
			}
		} else {
			if err := builder.LinkPads("audio decoder", builder.GetSrcPad(a.src), "audio encoder", a.encoder.GetStaticPad("sink")); err != nil {
				return nil, err
			}
		}
		srcPad = a.encoder.GetStaticPad("src")
	} else if a.mixer != nil {
		srcPad = builder.GetSrcPad(a.mixer)
	} else {
		srcPad = builder.GetSrcPad(a.src)
	}

	return gst.NewGhostPad("audio_src", srcPad), nil
}
