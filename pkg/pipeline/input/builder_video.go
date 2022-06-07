package input

import (
	"fmt"
	"strings"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/egress/pkg/pipeline/source"
)

func (b *Bin) buildVideoElements(p *params.Params) error {
	if !p.VideoEnabled {
		return nil
	}

	var err error
	if p.IsWebSource {
		err = b.buildWebVideoInput(p)
	} else {
		err = b.buildSDKVideoInput(p)
	}
	if err != nil {
		return err
	}

	b.videoQueue, err = gst.NewElement("queue")
	if err != nil {
		return err
	}
	if err = b.videoQueue.SetProperty("max-size-time", uint64(3e9)); err != nil {
		return err
	}

	b.videoElements = append(b.videoElements, b.videoQueue)
	return b.bin.AddMany(b.videoElements...)
}

func (b *Bin) buildWebVideoInput(p *params.Params) error {
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

	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return err
	}

	videoFramerateCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return err
	}
	if err = videoFramerateCaps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw,framerate=%d/1", p.Framerate),
	)); err != nil {
		return err
	}

	b.videoElements = append(b.videoElements, xImageSrc, videoConvert, videoFramerateCaps)

	return b.buildVideoEncoder(p)
}

// TODO: skip decoding when possible
func (b *Bin) buildSDKVideoInput(p *params.Params) error {
	src, codec := b.Source.(*source.SDKSource).GetVideoSource()

	src.Element.SetArg("format", "time")
	if err := src.Element.SetProperty("is-live", true); err != nil {
		return err
	}

	switch {
	case strings.EqualFold(codec.MimeType, string(params.MimeTypeH264)):
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

		b.videoElements = append(b.videoElements, src.Element, rtpH264Depay, avDecH264)

	case strings.EqualFold(codec.MimeType, string(params.MimeTypeVP8)):
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

		if p.OutputType == params.OutputTypeIVF {
			b.videoElements = append(b.videoElements, src.Element, rtpVP8Depay)
			return nil
		}

		vp8Dec, err := gst.NewElement("vp8dec")
		if err != nil {
			return nil
		}

		b.videoElements = append(b.videoElements, src.Element, rtpVP8Depay, vp8Dec)

	default:
		return errors.ErrNotSupported(codec.MimeType)
	}

	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return err
	}

	videoScale, err := gst.NewElement("videoscale")
	if err != nil {
		return err
	}

	videoRate, err := gst.NewElement("videorate")
	if err != nil {
		return err
	}

	decodedCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return err
	}
	if err = decodedCaps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw,format=I420,width=%d,height=%d,framerate=%d/1,colorimetry=bt709,chroma-site=mpeg2,pixel-aspect-ratio=1/1", p.Width, p.Height, p.Framerate)),
	); err != nil {
		return err
	}

	b.videoElements = append(b.videoElements, videoConvert, videoScale, videoRate, decodedCaps)

	return b.buildVideoEncoder(p)
}

func (b *Bin) buildVideoEncoder(p *params.Params) error {
	switch p.VideoCodec {
	// we only encode h264, the rest are too slow
	case params.MimeTypeH264:
		x264Enc, err := gst.NewElement("x264enc")
		if err != nil {
			return err
		}
		if err = x264Enc.SetProperty("bitrate", uint(p.VideoBitrate)); err != nil {
			return err
		}
		x264Enc.SetArg("speed-preset", "veryfast")
		x264Enc.SetArg("tune", "zerolatency")
		if p.OutputType == params.OutputTypeHLS {
			x264Enc.SetProperty("key-int-max", uint(int32(p.SegmentDuration)*p.Framerate))
			// Avoid key frames other than at segments boudaries as splitmuxsink can become inconsistent otherwise
			x264Enc.SetProperty("option-string", "scenecut=0")
		}

		if p.VideoProfile == "" {
			p.VideoProfile = params.ProfileMain
		}

		encodedCaps, err := gst.NewElement("capsfilter")
		if err != nil {
			return err
		}

		if err = encodedCaps.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf("video/x-h264,profile=%s,framerate=%d/1", p.VideoProfile, p.Framerate),
		)); err != nil {
			return err
		}

		b.videoElements = append(b.videoElements, x264Enc, encodedCaps)
		return nil

	default:
		return errors.ErrNotSupported(fmt.Sprintf("%s encoding", p.VideoCodec))
	}
}
