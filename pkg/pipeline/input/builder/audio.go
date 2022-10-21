package builder

import (
	"fmt"
	"strings"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/logger"
)

type AudioInput struct {
	decoder []*gst.Element
	testSrc []*gst.Element
	mixer   []*gst.Element
	encoder *gst.Element
}

func NewWebAudioInput(p *params.Params) (*AudioInput, error) {
	a := &AudioInput{}

	if err := a.buildWebDecoder(p); err != nil {
		return nil, err
	}
	if err := a.buildEncoder(p); err != nil {
		return nil, err
	}

	return a, nil
}

func NewSDKAudioInput(p *params.Params, src *app.Source, codec webrtc.RTPCodecParameters) (*AudioInput, error) {
	a := &AudioInput{}

	if err := a.buildSDKDecoder(p, src, codec); err != nil {
		return nil, err
	}
	if err := a.buildMixer(p); err != nil {
		return nil, err
	}
	if p.OutputType == params.OutputTypeRaw {
		return a, nil
	}
	if err := a.buildEncoder(p); err != nil {
		return nil, err
	}

	return a, nil
}

func (a *AudioInput) AddToBin(bin *gst.Bin) error {
	if a.decoder != nil {
		if err := bin.AddMany(a.decoder...); err != nil {
			return err
		}
	}
	if a.testSrc != nil {
		if err := bin.AddMany(a.testSrc...); err != nil {
			return err
		}
	}
	if a.mixer != nil {
		if err := bin.AddMany(a.mixer...); err != nil {
			return err
		}
	}
	if a.encoder != nil {
		if err := bin.Add(a.encoder); err != nil {
			return err
		}
	}
	return nil
}

func (a *AudioInput) Link() error {
	if a.decoder != nil {
		if err := gst.ElementLinkMany(a.decoder...); err != nil {
			return err
		}
	}
	if a.testSrc != nil {
		if err := gst.ElementLinkMany(a.testSrc...); err != nil {
			return err
		}
	}
	if a.mixer != nil {
		if link := getSrcPad(a.decoder).Link(a.mixer[0].GetRequestPad("sink_%u")); link != gst.PadLinkOK {
			return errors.ErrPadLinkFailed("audio decoder", "audio mixer", link.String())
		}

		if link := getSrcPad(a.testSrc).Link(a.mixer[0].GetRequestPad("sink_%u")); link != gst.PadLinkOK {
			return errors.ErrPadLinkFailed("audio test src", "audio mixer", link.String())
		}
		if err := gst.ElementLinkMany(a.mixer...); err != nil {
			return err
		}
	}
	if a.encoder != nil {
		if a.mixer != nil {
			if link := getSrcPad(a.mixer).Link(a.encoder.GetStaticPad("sink")); link != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("audio mixer", "audio encoder", link.String())
			}
		} else {
			if link := getSrcPad(a.decoder).Link(a.encoder.GetStaticPad("sink")); link != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("audio decoder", "audio encoder", link.String())
			}
		}
	}

	return nil
}

func (a *AudioInput) GetSrcPad() *gst.Pad {
	if a.encoder != nil {
		return a.encoder.GetStaticPad("src")
	}
	if a.mixer != nil {
		return getSrcPad(a.mixer)
	}
	return getSrcPad(a.decoder)
}

func (a *AudioInput) buildWebDecoder(p *params.Params) error {
	pulseSrc, err := gst.NewElement("pulsesrc")
	if err != nil {
		return err
	}
	if err = pulseSrc.SetProperty("device", fmt.Sprintf("%s.monitor", p.Info.EgressId)); err != nil {
		return err
	}

	a.decoder = []*gst.Element{pulseSrc}
	return a.addConverter(p)
}

func (a *AudioInput) buildSDKDecoder(p *params.Params, src *app.Source, codec webrtc.RTPCodecParameters) error {
	src.Element.SetArg("format", "time")
	if err := src.Element.SetProperty("is-live", true); err != nil {
		return err
	}
	a.decoder = []*gst.Element{src.Element}

	switch {
	case strings.EqualFold(codec.MimeType, string(params.MimeTypeOpus)):
		if err := src.Element.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf(
				"application/x-rtp,media=audio,payload=%d,encoding-name=OPUS,clock-rate=%d",
				codec.PayloadType, codec.ClockRate,
			),
		)); err != nil {
			return err
		}

		rtpOpusDepay, err := gst.NewElement("rtpopusdepay")
		if err != nil {
			return err
		}

		opusDec, err := gst.NewElement("opusdec")
		if err != nil {
			return err
		}
		if err = opusDec.SetProperty("use-inband-fec", true); err != nil {
			return err
		}

		a.decoder = append(a.decoder, rtpOpusDepay, opusDec)

	default:
		return errors.ErrNotSupported(codec.MimeType)
	}

	return a.addConverter(p)
}

func (a *AudioInput) addConverter(p *params.Params) error {
	audioQueue, err := buildQueue()
	if err != nil {
		return err
	}

	audioConvert, err := gst.NewElement("audioconvert")
	if err != nil {
		return err
	}

	// TODO: sinc-filter-mode=full will use more memory but much less CPU
	audioResample, err := gst.NewElement("audioresample")
	if err != nil {
		return err
	}

	capsFilter, err := getCapsFilter(p)
	if err != nil {
		return err
	}

	a.decoder = append(a.decoder, audioQueue, audioConvert, audioResample, capsFilter)
	return nil
}

func (a *AudioInput) buildMixer(p *params.Params) error {
	audioTestSrc, err := gst.NewElement("audiotestsrc")
	if err != nil {
		return err
	}
	if err = audioTestSrc.SetProperty("volume", 0.0); err != nil {
		return err
	}
	if err = audioTestSrc.SetProperty("do-timestamp", true); err != nil {
		return err
	}
	if err = audioTestSrc.SetProperty("is-live", true); err != nil {
		return err
	}
	audioCaps, err := getCapsFilter(p)
	if err != nil {
		return err
	}
	a.testSrc = []*gst.Element{audioTestSrc, audioCaps}

	audioMixer, err := gst.NewElement("audiomixer")
	if err != nil {
		return err
	}
	// set latency slightly higher than max audio appsrc latency
	if err = audioMixer.SetProperty("latency", latency); err != nil {
		logger.Errorw("latency", err)
		return err
	}
	mixedCaps, err := getCapsFilter(p)
	if err != nil {
		return err
	}
	a.mixer = []*gst.Element{audioMixer, mixedCaps}

	return nil
}

func (a *AudioInput) buildEncoder(p *params.Params) error {
	switch p.AudioCodec {
	case params.MimeTypeOpus:
		encoder, err := gst.NewElement("opusenc")
		if err != nil {
			return err
		}
		if err = encoder.SetProperty("bitrate", int(p.AudioBitrate*1000)); err != nil {
			return err
		}
		a.encoder = encoder

	case params.MimeTypeAAC:
		encoder, err := gst.NewElement("faac")
		if err != nil {
			return err
		}
		if err = encoder.SetProperty("bitrate", int(p.AudioBitrate*1000)); err != nil {
			return err
		}
		a.encoder = encoder

	default:
		return errors.ErrNotSupported(string(p.AudioCodec))
	}

	return nil
}

func getCapsFilter(p *params.Params) (*gst.Element, error) {
	var caps *gst.Caps
	switch p.AudioCodec {
	case params.MimeTypeOpus, params.MimeTypeRaw:
		caps = gst.NewCapsFromString(
			"audio/x-raw,format=S16LE,layout=interleaved,rate=48000,channels=2",
		)
	case params.MimeTypeAAC:
		caps = gst.NewCapsFromString(
			fmt.Sprintf("audio/x-raw,format=S16LE,layout=interleaved,rate=%d,channels=2", p.AudioFrequency),
		)
	default:
		return nil, errors.ErrNotSupported(string(p.AudioCodec))
	}

	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, err
	}
	if err = capsFilter.SetProperty("caps", caps); err != nil {
		return nil, err
	}

	return capsFilter, nil
}
