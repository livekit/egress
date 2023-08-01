package input

import (
	"fmt"
	"strings"
	"unsafe"

	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/builder"
	"github.com/livekit/egress/pkg/types"
)

type videoInput struct {
	src      []*gst.Element
	testSrc  *gst.Element
	selector *gst.Element
	encoder  []*gst.Element
}

func (v *videoInput) buildWebInput(p *config.PipelineConfig) error {
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

	videoQueue, err := builder.BuildQueue("video_input_queue", p.Latency, true)
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

	v.src = []*gst.Element{xImageSrc, videoQueue, videoConvert, caps}
	return nil
}

func (v *videoInput) buildSDKInput(p *config.PipelineConfig) error {
	if p.VideoSrc != nil {
		if err := v.buildAppSource(p); err != nil {
			return err
		}
		return nil
	}

	if err := v.buildTestSrc(); err != nil {
		return err
	}

	if err := v.buildInputSelector(); err != nil {
		return err
	}

	return nil
}

func (v *videoInput) buildAppSource(p *config.PipelineConfig) error {
	src := p.VideoSrc
	src.Element.SetArg("format", "time")
	if err := src.Element.SetProperty("is-live", true); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	v.src = append(v.src, src.Element)
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
		v.src = append(v.src, rtpH264Depay)

		if p.VideoTranscoding {
			avDecH264, err := gst.NewElement("avdec_h264")
			if err != nil {
				return errors.ErrGstPipelineError(err)
			}

			v.src = append(v.src, avDecH264)
		} else {
			h264parse, err := gst.NewElement("h264parse")
			if err != nil {
				return errors.ErrGstPipelineError(err)
			}

			v.src = append(v.src, h264parse)

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
		v.src = append(v.src, rtpVP8Depay)

		if !p.VideoTranscoding {
			return nil
		}

		vp8Dec, err := gst.NewElement("vp8dec")
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}

		v.src = append(v.src, vp8Dec)

	default:
		return errors.ErrNotSupported(p.VideoCodecParams.MimeType)
	}

	videoQueue, err := builder.BuildQueue("video_input_queue", p.Latency, true)
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

	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = caps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw,framerate=%d/1,format=I420,width=%d,height=%d,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1",
			p.Framerate, p.Width, p.Height,
		)),
	); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	v.src = append(v.src, videoQueue, videoConvert, videoScale, videoRate, caps)
	return nil
}

func (v *videoInput) buildTestSrc() error {
	videoTestSrc, err := gst.NewElement("videotestsrc")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = videoTestSrc.SetProperty("is-live", true); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	videoTestSrc.SetArg("pattern", "black")

	v.testSrc = videoTestSrc
	return nil
}

func (v *videoInput) buildInputSelector() error {
	inputSelector, err := gst.NewElement("input-selector")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err = inputSelector.SetProperty("drop-backwards", true); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	v.selector = inputSelector
	return nil
}

func (v *videoInput) buildEncoder(p *config.PipelineConfig) error {
	// Put a queue in front of the encoder for pipelining with the stage before
	videoQueue, err := builder.BuildQueue("video_encoder_queue", p.Latency, false)
	if err != nil {
		return err
	}
	v.encoder = append(v.encoder, videoQueue)

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

		if p.GetSegmentConfig() != nil {
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

		v.encoder = append(v.encoder, x264Enc, caps)
		return nil

	default:
		return errors.ErrNotSupported(fmt.Sprintf("%s encoding", p.VideoOutCodec))
	}
}

// TODO: ignores selector for now
func (v *videoInput) link() (*gst.GhostPad, error) {
	elements := append(v.src, v.encoder...)

	err := gst.ElementLinkMany(elements...)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	return gst.NewGhostPad("video_src", builder.GetSrcPad(elements)), nil
}

func (v *videoInput) setSelectorPad(pad *gst.Pad) error {
	pt, err := v.selector.GetPropertyType("active-pad")
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}

	val, err := glib.ValueInit(pt)
	if err != nil {
		return errors.ErrGstPipelineError(err)
	}
	val.SetInstance(uintptr(unsafe.Pointer(pad.Instance())))

	if err = v.selector.SetPropertyValue("active-pad", val); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	return nil
}
