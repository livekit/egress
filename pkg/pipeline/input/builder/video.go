package builder

import (
	"fmt"
	"strings"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
)

type VideoInput struct {
	elements []*gst.Element
}

func NewWebVideoInput(p *config.PipelineConfig) (*VideoInput, error) {
	v := &VideoInput{}

	if err := v.buildWebDecoder(p); err != nil {
		return nil, err
	}
	if err := v.buildEncoder(p); err != nil {
		return nil, err
	}
	return v, nil
}

func NewSDKVideoInput(p *config.PipelineConfig, src *app.Source, codec webrtc.RTPCodecParameters) (*VideoInput, error) {
	v := &VideoInput{}

	if err := v.buildSDKDecoder(p, src, codec); err != nil {
		return nil, err
	}
	if p.OutputType == types.OutputTypeIVF || p.OutputType == types.OutputTypeWebM {
		return v, nil
	}
	if err := v.buildEncoder(p); err != nil {
		return nil, err
	}

	return v, nil
}

func (v *VideoInput) AddToBin(bin *gst.Bin) error {
	return bin.AddMany(v.elements...)
}

func (v *VideoInput) Link() error {
	return gst.ElementLinkMany(v.elements...)
}

func (v *VideoInput) GetSrcPad() *gst.Pad {
	return getSrcPad(v.elements)
}

func (v *VideoInput) buildWebDecoder(p *config.PipelineConfig) error {
	xImageSrc, err := gst.NewElement("ximagesrc")
	if err != nil {
		return err
	}
	if err = xImageSrc.SetProperty("display-name", p.Display); err != nil {
		return err
	}
	if err = xImageSrc.SetProperty("use-damage", false); err != nil {
		return err
	}
	if err = xImageSrc.SetProperty("show-pointer", false); err != nil {
		return err
	}

	videoQueue, err := buildQueue(Latency/10, true)
	if err != nil {
		return err
	}

	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return err
	}

	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return err
	}
	if err = caps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw,framerate=%d/1", p.Framerate),
	)); err != nil {
		return err
	}

	v.elements = []*gst.Element{xImageSrc, videoQueue, videoConvert, caps}
	return nil
}

func (v *VideoInput) buildSDKDecoder(p *config.PipelineConfig, src *app.Source, codec webrtc.RTPCodecParameters) error {
	src.Element.SetArg("format", "time")
	if err := src.Element.SetProperty("is-live", true); err != nil {
		return err
	}

	v.elements = append(v.elements, src.Element)
	switch {
	case strings.EqualFold(codec.MimeType, string(types.MimeTypeH264)):
		if err := src.Element.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf(
				"application/x-rtp,media=video,payload=%d,encoding-name=H264,clock-rate=%d",
				codec.PayloadType, codec.ClockRate,
			),
		)); err != nil {
			return err
		}

		rtpH264Depay, err := gst.NewElement("rtph264depay")
		if err != nil {
			return err
		}

		avDecH264, err := gst.NewElement("avdec_h264")
		if err != nil {
			return err
		}

		v.elements = append(v.elements, rtpH264Depay, avDecH264)

	case strings.EqualFold(codec.MimeType, string(types.MimeTypeVP8)):
		if err := src.Element.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf(
				"application/x-rtp,media=video,payload=%d,encoding-name=VP8,clock-rate=%d",
				codec.PayloadType, codec.ClockRate,
			),
		)); err != nil {
			return err
		}

		rtpVP8Depay, err := gst.NewElement("rtpvp8depay")
		if err != nil {
			return err
		}

		if p.OutputType == types.OutputTypeIVF || p.OutputType == types.OutputTypeWebM {
			v.elements = append(v.elements, rtpVP8Depay)
			return nil
		}

		vp8Dec, err := gst.NewElement("vp8dec")
		if err != nil {
			return err
		}

		v.elements = append(v.elements, rtpVP8Depay, vp8Dec)

	default:
		return errors.ErrNotSupported(codec.MimeType)
	}

	videoQueue, err := buildQueue(Latency/10, true)
	if err != nil {
		return err
	}

	videoConvertScale, err := gst.NewElement("videoconvertscale")
	if err != nil {
		return err
	}

	videoRate, err := gst.NewElement("videorate")
	if err != nil {
		return err
	}
	if err = videoRate.SetProperty("max-rate", int(p.Framerate)); err != nil {
		return err
	}

	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return err
	}
	if err = caps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw,format=I420,width=%d,height=%d,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1", p.Width, p.Height)),
	); err != nil {
		return err
	}

	v.elements = append(v.elements, videoQueue, videoConvertScale, videoRate, caps)
	return nil
}

func (v *VideoInput) buildEncoder(p *config.PipelineConfig) error {
	// Put a queue in front of the encoder for pipelineing with the stage before
	videoQueue, err := buildQueue(Latency/10, false)
	if err != nil {
		return err
	}
	v.elements = append(v.elements, videoQueue)

	switch p.VideoCodec {
	// we only encode h264, the rest are too slow
	case types.MimeTypeH264:
		x264Enc, err := gst.NewElement("x264enc")
		if err != nil {
			return err
		}
		if err = x264Enc.SetProperty("bitrate", uint(p.VideoBitrate)); err != nil {
			return err
		}
		x264Enc.SetArg("speed-preset", "veryfast")
		if p.OutputType == types.OutputTypeHLS {
			// The muxer should request key frames to match the segment duration. Set a 2 x segment duration on the encoder as a safeguard.
			if err = x264Enc.SetProperty("key-int-max", uint(int32(p.SegmentDuration)*p.Framerate*2)); err != nil {
				return err
			}
			// Avoid key frames other than at segments boundaries as splitmuxsink can become inconsistent otherwise
			if err = x264Enc.SetProperty("option-string", "scenecut=0"); err != nil {
				return err
			}
		}

		caps, err := gst.NewElement("capsfilter")
		if err != nil {
			return err
		}

		if err = caps.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf("video/x-h264,profile=%s", p.VideoProfile),
		)); err != nil {
			return err
		}

		v.elements = append(v.elements, x264Enc, caps)
		return nil

	default:
		return errors.ErrNotSupported(fmt.Sprintf("%s encoding", p.VideoCodec))
	}
}
