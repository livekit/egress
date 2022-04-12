package input

import (
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/livekit-egress/pkg/pipeline/source"
)

func (b *Bin) buildAudioElements(p *params.Params) error {
	if !p.AudioEnabled {
		return nil
	}

	var err error
	if p.IsWebInput {
		err = b.buildWebAudioInput(p)
	} else {
		err = b.buildSDKAudioInput(p)
	}
	if err != nil {
		return err
	}

	b.audioQueue, err = gst.NewElement("queue")
	if err != nil {
		return err
	}
	if err = b.audioQueue.SetProperty("max-size-time", uint64(3e9)); err != nil {
		return err
	}
	b.audioElements = append(b.audioElements, b.audioQueue)

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

	audioConvert, err := gst.NewElement("audioconvert")
	if err != nil {
		return err
	}

	var capsStr string
	var encoder *gst.Element
	switch p.AudioCodec {
	case livekit.AudioCodec_OPUS:
		capsStr = "audio/x-raw,format=S16LE,layout=interleaved,rate=48000,channels=2"

		encoder, err = gst.NewElement("opusenc")
		if err != nil {
			return err
		}

	case livekit.AudioCodec_AAC:
		capsStr = fmt.Sprintf("audio/x-raw,format=S16LE,layout=interleaved,rate=%d,channels=2", p.AudioFrequency)

		encoder, err = gst.NewElement("faac")
		if err != nil {
			return err
		}
	}

	audioCapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return err
	}
	if err = audioCapsFilter.SetProperty("caps", gst.NewCapsFromString(capsStr)); err != nil {
		return err
	}

	if err = encoder.SetProperty("bitrate", int(p.AudioBitrate*1000)); err != nil {
		return err
	}

	b.audioElements = append(b.audioElements, pulseSrc, audioConvert, audioCapsFilter, encoder)
	return nil
}

func (b *Bin) buildSDKAudioInput(p *params.Params) error {
	b.audioSrc.SetDoTimestamp(true)
	b.audioSrc.SetFormat(gst.FormatTime)
	b.audioSrc.SetLive(true)

	mimeType := <-b.audioMimeType
	switch mimeType {
	case source.MimeTypeOpus:
		if err := b.audioSrc.Element.SetProperty("caps", gst.NewCapsFromString(
			"application/x-rtp,media=audio,payload=111,encoding-name=OPUS,clock-rate=48000",
		)); err != nil {
			return err
		}

		rtpJitterBuffer, err := gst.NewElement("rtpjitterbuffer")
		if err != nil {
			return err
		}
		rtpJitterBuffer.SetArg("mode", "none")

		rtpOpusDepay, err := gst.NewElement("rtpopusdepay")
		if err != nil {
			return err
		}

		b.audioElements = append(b.audioElements, b.audioSrc.Element, rtpJitterBuffer, rtpOpusDepay)

		switch p.AudioCodec {
		case livekit.AudioCodec_OPUS:

		case livekit.AudioCodec_AAC:
			opusDec, err := gst.NewElement("opusdec")
			if err != nil {
				return err
			}

			audioConvert, err := gst.NewElement("audioconvert")
			if err != nil {
				return err
			}

			audioResample, err := gst.NewElement("audioresample")
			if err != nil {
				return err
			}

			audioCapsFilter, err := gst.NewElement("capsfilter")
			if err != nil {
				return err
			}
			if err = audioCapsFilter.SetProperty("caps", gst.NewCapsFromString(
				fmt.Sprintf("audio/x-raw,format=S16LE,layout=interleaved,rate=%d,channels=2", p.AudioFrequency),
			)); err != nil {
				return err
			}

			faac, err := gst.NewElement("faac")
			if err != nil {
				return err
			}
			if err = faac.SetProperty("bitrate", int(p.AudioBitrate*1000)); err != nil {
				return err
			}

			b.audioElements = append(b.audioElements, opusDec, audioConvert, audioResample, audioCapsFilter, faac)
		}
	default:
		return errors.ErrNotSupported(mimeType)
	}

	return nil
}
