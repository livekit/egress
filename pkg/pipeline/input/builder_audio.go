package input

import (
	"fmt"
	"strings"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/egress/pkg/pipeline/source"
)

func (b *Bin) buildAudioElements(p *params.Params) error {
	if !p.AudioEnabled {
		return nil
	}

	var err error
	if p.IsWebSource {
		err = b.buildWebAudioInput(p)
	} else {
		err = b.buildSDKAudioInput(p)
	}
	if err != nil {
		return err
	}

	return b.bin.AddMany(b.audioElements...)
}

func (b *Bin) buildWebAudioInput(p *params.Params) error {
	pulseSrc, err := gst.NewElement("pulsesrc")
	if err != nil {
		return err
	}
	if err = pulseSrc.SetProperty("device", fmt.Sprintf("%s.monitor", p.Info.EgressId)); err != nil {
		return err
	}

	audioQueue, err := gst.NewElement("queue")
	if err != nil {
		return err
	}
	if err = audioQueue.SetProperty("max-size-time", uint64(3e9)); err != nil {
		return err
	}

	b.audioElements = append(b.audioElements, pulseSrc, audioQueue)

	return b.buildAudioEncoder(p)
}

// TODO: skip decoding when possible
func (b *Bin) buildSDKAudioInput(p *params.Params) error {
	src, codec := b.Source.(*source.SDKSource).GetAudioSource()

	src.Element.SetArg("format", "time")
	if err := src.Element.SetProperty("is-live", true); err != nil {
		return err
	}

	audioQueue, err := gst.NewElement("queue")
	if err != nil {
		return err
	}
	if err = audioQueue.SetProperty("max-size-time", uint64(3e9)); err != nil {
		return err
	}

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

		b.audioElements = append(b.audioElements, src.Element, audioQueue, rtpOpusDepay, opusDec)

		// skip encoding for raw output
		if p.OutputType == params.OutputTypeRaw {
			return nil
		}

		return b.buildAudioEncoder(p)

	default:
		return errors.ErrNotSupported(codec.MimeType)
	}
}

func (b *Bin) buildAudioEncoder(p *params.Params) error {
	audioConvert, err := gst.NewElement("audioconvert")
	if err != nil {
		return err
	}

	// TODO: sinc-filter-mode=full will use more memory but much less CPU
	audioResample, err := gst.NewElement("audioresample")
	if err != nil {
		return err
	}

	var audioCaps string
	var encoder *gst.Element
	switch p.AudioCodec {
	case params.MimeTypeOpus:
		audioCaps = "audio/x-raw,format=S16LE,layout=interleaved,rate=48000,channels=2"
		encoder, err = gst.NewElement("opusenc")
		if err != nil {
			return err
		}
		if err = encoder.SetProperty("bitrate", int(p.AudioBitrate*1000)); err != nil {
			return err
		}

	case params.MimeTypeAAC:
		audioCaps = fmt.Sprintf("audio/x-raw,format=S16LE,layout=interleaved,rate=%d,channels=2", p.AudioFrequency)
		encoder, err = gst.NewElement("faac")
		if err != nil {
			return err
		}
		if err = encoder.SetProperty("bitrate", int(p.AudioBitrate*1000)); err != nil {
			return err
		}
	}

	audioCapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return err
	}
	if err = audioCapsFilter.SetProperty("caps", gst.NewCapsFromString(audioCaps)); err != nil {
		return err
	}

	b.audioElements = append(b.audioElements, audioConvert, audioResample, audioCapsFilter, encoder)
	return nil
}
