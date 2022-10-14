package sdk

import (
	"context"
	"fmt"
	"strings"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/input/bin"
	"github.com/livekit/egress/pkg/pipeline/params"
)

func (s *SDKInput) build(ctx context.Context, p *params.Params) error {
	audio, err := buildAudioElements(s.audioSrc, s.audioCodec, p)
	if err != nil {
		return err
	}
	video, err := buildVideoElements(s.videoSrc, s.videoCodec, p)
	if err != nil {
		return err
	}

	s.InputBin = bin.New(audio, video)
	return s.InputBin.Build(ctx, p)
}

func buildAudioElements(src *app.Source, codec webrtc.RTPCodecParameters, p *params.Params) ([]*gst.Element, error) {
	if !p.AudioEnabled {
		return nil, nil
	}

	src.Element.SetArg("format", "time")
	if err := src.Element.SetProperty("is-live", true); err != nil {
		return nil, err
	}

	audioQueue, err := bin.BuildQueue()
	if err != nil {
		return nil, err
	}

	switch {
	case strings.EqualFold(codec.MimeType, string(params.MimeTypeOpus)):
		if err = src.Element.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf(
				"application/x-rtp,media=audio,payload=%d,encoding-name=OPUS,clock-rate=%d",
				codec.PayloadType, codec.ClockRate,
			),
		)); err != nil {
			return nil, err
		}

		rtpOpusDepay, err := gst.NewElement("rtpopusdepay")
		if err != nil {
			return nil, err
		}

		opusDec, err := gst.NewElement("opusdec")
		if err != nil {
			return nil, err
		}

		audioElements := []*gst.Element{src.Element, rtpOpusDepay, opusDec, audioQueue}

		// skip encoding for raw output
		if p.OutputType == params.OutputTypeRaw {
			return audioElements, nil
		}

		encoder, err := bin.BuildAudioEncoder(p)
		audioElements = append(audioElements, encoder...)

		return audioElements, nil

	default:
		return nil, errors.ErrNotSupported(codec.MimeType)
	}
}

func buildVideoElements(src *app.Source, codec webrtc.RTPCodecParameters, p *params.Params) ([]*gst.Element, error) {
	if !p.VideoEnabled {
		return nil, nil
	}

	src.Element.SetArg("format", "time")
	if err := src.Element.SetProperty("is-live", true); err != nil {
		return nil, err
	}

	elements := []*gst.Element{src.Element}
	switch {
	case strings.EqualFold(codec.MimeType, string(params.MimeTypeH264)):
		if err := src.Element.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf(
				"application/x-rtp,media=video,payload=%d,encoding-name=H264,clock-rate=%d",
				codec.PayloadType, codec.ClockRate,
			),
		)); err != nil {
			return nil, err
		}

		rtpH264Depay, err := gst.NewElement("rtph264depay")
		if err != nil {
			return nil, err
		}

		avDecH264, err := gst.NewElement("avdec_h264")
		if err != nil {
			return nil, err
		}

		elements = append(elements, rtpH264Depay, avDecH264)

	case strings.EqualFold(codec.MimeType, string(params.MimeTypeVP8)):
		if err := src.Element.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf(
				"application/x-rtp,media=video,payload=%d,encoding-name=VP8,clock-rate=%d",
				codec.PayloadType, codec.ClockRate,
			),
		)); err != nil {
			return nil, err
		}

		rtpVP8Depay, err := gst.NewElement("rtpvp8depay")
		if err != nil {
			return nil, err
		}

		if p.OutputType == params.OutputTypeIVF || p.OutputType == params.OutputTypeWebM {
			return append(elements, rtpVP8Depay), nil
		}

		vp8Dec, err := gst.NewElement("vp8dec")
		if err != nil {
			return nil, err
		}

		elements = append(elements, rtpVP8Depay, vp8Dec)

	default:
		return nil, errors.ErrNotSupported(codec.MimeType)
	}

	videoQueue, err := bin.BuildQueue()
	if err != nil {
		return nil, err
	}

	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, err
	}

	videoScale, err := gst.NewElement("videoscale")
	if err != nil {
		return nil, err
	}

	videoRate, err := gst.NewElement("videorate")
	if err != nil {
		return nil, err
	}

	decodedCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, err
	}
	if err = decodedCaps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw,format=I420,width=%d,height=%d,framerate=%d/1,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1", p.Width, p.Height, p.Framerate)),
	); err != nil {
		return nil, err
	}

	elements = append(elements, videoQueue, videoConvert, videoScale, videoRate, decodedCaps)

	encoder, err := bin.BuildVideoEncoder(p)
	if err != nil {
		return nil, err
	}

	return append(elements, encoder...), nil
}
