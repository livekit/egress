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

type VideoInput struct {
	elements []*gst.Element
}

func (b *Bin) buildVideoInput(p *config.PipelineConfig) error {
	v := &VideoInput{}

	switch p.SourceType {
	case types.SourceTypeSDK:
		if err := v.buildSDKDecoder(p); err != nil {
			return err
		}

	case types.SourceTypeWeb:
		if err := v.buildWebDecoder(p); err != nil {
			return err
		}
	}

	if p.VideoTranscoding {
		if err := v.buildEncoder(p); err != nil {
			return err
		}
	}

	// Add elements to bin
	if err := b.bin.AddMany(v.elements...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	b.video = v
	return nil
}

func (v *VideoInput) Link() (*gst.GhostPad, error) {
	err := gst.ElementLinkMany(v.elements...)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return gst.NewGhostPad("video_src", v.elements[len(v.elements)-1].GetStaticPad("src")), nil
}

func (v *VideoInput) GetSrcPad() *gst.Pad {
	return builder.GetSrcPad(v.elements)
}

func (v *VideoInput) buildWebDecoder(p *config.PipelineConfig) error {
	xImageSrc, err := gst.NewElement("ximagesrc")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = xImageSrc.SetProperty("display-name", p.Display); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = xImageSrc.SetProperty("use-damage", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = xImageSrc.SetProperty("show-pointer", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	videoQueue, err := builder.BuildQueue("video_input_queue", true)
	if err != nil {
		return err
	}

	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = caps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw,framerate=%d/1", p.Framerate),
	)); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	v.elements = []*gst.Element{xImageSrc, videoQueue, videoConvert, caps}
	return nil
}

func (v *VideoInput) buildSDKDecoder(p *config.PipelineConfig) error {
	src := p.VideoSrc
	src.Element.SetArg("format", "time")
	if err := src.Element.SetProperty("is-live", true); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	v.elements = append(v.elements, src.Element)
	switch {
	case strings.EqualFold(p.VideoCodecParams.MimeType, string(types.MimeTypeH264)):
		if err := src.Element.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf(
				"application/x-rtp,media=video,payload=%d,encoding-name=H264,clock-rate=%d",
				p.VideoCodecParams.PayloadType, p.VideoCodecParams.ClockRate,
			),
		)); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		rtpH264Depay, err := gst.NewElement("rtph264depay")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		v.elements = append(v.elements, rtpH264Depay)

		if p.VideoTranscoding {
			avDecH264, err := gst.NewElement("avdec_h264")
			if err != nil {
				return errors.ErrGstPipelineError(err)
			}

			v.elements = append(v.elements, avDecH264)
		} else {
			h264parse, err := gst.NewElement("h264parse")
			if err != nil {
				return errors.ErrGstPipelineError(err)
			}

			v.elements = append(v.elements, h264parse)

			return nil
		}

	case strings.EqualFold(p.VideoCodecParams.MimeType, string(types.MimeTypeVP8)):
		if err := src.Element.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf(
				"application/x-rtp,media=video,payload=%d,encoding-name=VP8,clock-rate=%d",
				p.VideoCodecParams.PayloadType, p.VideoCodecParams.ClockRate,
			),
		)); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		rtpVP8Depay, err := gst.NewElement("rtpvp8depay")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		v.elements = append(v.elements, rtpVP8Depay)

		if !p.VideoTranscoding {
			return nil
		}

		vp8Dec, err := gst.NewElement("vp8dec")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		v.elements = append(v.elements, vp8Dec)

	default:
		return errors.ErrNotSupported(p.VideoCodecParams.MimeType)
	}

	videoQueue, err := builder.BuildQueue("video_input_queue", true)
	if err != nil {
		return err
	}

	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	videoScale, err := gst.NewElement("videoscale")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	videoRate, err := gst.NewElement("videorate")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = videoRate.SetProperty("max-rate", int(p.Framerate)); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = caps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw,format=I420,width=%d,height=%d,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1", p.Width, p.Height)),
	); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	v.elements = append(v.elements, videoQueue, videoConvert, videoScale, videoRate, caps)
	return nil
}

func (v *VideoInput) buildEncoder(p *config.PipelineConfig) error {
	// Put a queue in front of the encoder for pipelining with the stage before
	videoQueue, err := builder.BuildQueue("video_encoder_queue", false)
	if err != nil {
		return err
	}
	v.elements = append(v.elements, videoQueue)

	switch p.VideoOutCodec {
	// we only encode h264, the rest are too slow
	case types.MimeTypeH264:
		x264Enc, err := gst.NewElement("x264enc")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err = x264Enc.SetProperty("bitrate", uint(p.VideoBitrate)); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		x264Enc.SetArg("speed-preset", "veryfast")

		if p.KeyFrameInterval != 0 {
			if err = x264Enc.SetProperty("key-int-max", uint(p.KeyFrameInterval*float64(p.Framerate))); err != nil {
				return errors.ErrGstPipelineError(err)
			}
		}

		if _, ok := p.Outputs[types.EgressTypeSegments]; ok {
			// Avoid key frames other than at segments boundaries as splitmuxsink can become inconsistent otherwise
			if err = x264Enc.SetProperty("option-string", "scenecut=0"); err != nil {
				return errors.ErrGstPipelineError(err)
			}
		}

		caps, err := gst.NewElement("capsfilter")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		if err = caps.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf("video/x-h264,profile=%s", p.VideoProfile),
		)); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		v.elements = append(v.elements, x264Enc, caps)
		return nil

	default:
		return errors.ErrNotSupported(fmt.Sprintf("%s encoding", p.VideoOutCodec))
	}
}
