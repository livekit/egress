package input

import (
	"fmt"
	"strings"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/livekit-egress/pkg/pipeline/source"
)

func (b *Bin) buildVideoElements(p *params.Params) error {
	if !p.VideoEnabled {
		return nil
	}

	var err error
	if p.IsWebInput {
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

	switch p.VideoCodec {
	case livekit.VideoCodec_H264_BASELINE:
		err = b.buildH26XElements(264, "baseline", p)
	case livekit.VideoCodec_H264_MAIN:
		err = b.buildH26XElements(264, "main", p)
	case livekit.VideoCodec_H264_HIGH:
		err = b.buildH26XElements(264, "high", p)
	// case livekit.VideoCodec_VP8:
	//  // TODO: vp8 low quality/choppy
	// 	err = b.buildVPXElements(8, params)
	// case livekit.VideoCodec_VP9:
	//  // TODO: vp9 is extremely slow, audio gets dropped, default parameters cannot keep up with live source
	// 	err = b.buildVPXElements(9, params)
	// case livekit.VideoCodec_HEVC_MAIN:
	//  // TODO: hevc low quality/choppy
	// 	err = b.buildH26XElements(265, "main", params)
	// case livekit.VideoCodec_HEVC_HIGH:
	//  // TODO: hevc low quality/choppy
	// 	err = b.buildH26XElements(265, "main", params)
	default:
		err = errors.ErrNotSupported(p.VideoCodec.String())
	}

	return err
}

func (b *Bin) buildSDKVideoInput(p *params.Params) error {
	src, codec := b.Source.(*source.SDKSource).GetVideoSource()

	src.SetDoTimestamp(true)
	src.SetFormat(gst.FormatTime)
	src.SetLive(true)

	codecInfo := <-codec
	switch {
	case strings.EqualFold(codecInfo.MimeType, source.MimeTypeH264):
		if err := src.Element.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf(
				"application/x-rtp,media=video,payload=%d,encoding-name=H264,clock-rate=%d",
				codecInfo.PayloadType, codecInfo.ClockRate,
			),
		)); err != nil {
			return err
		}

		rtpJitterBuffer, err := gst.NewElement("rtpjitterbuffer")
		if err != nil {
			return err
		}
		rtpJitterBuffer.SetArg("mode", "none")

		rtpH264Depay, err := gst.NewElement("rtph264depay")
		if err != nil {
			return err
		}

		avDecH264, err := gst.NewElement("avdec_h264")
		if err != nil {
			return err
		}

		b.videoElements = append(b.videoElements, rtpJitterBuffer, rtpH264Depay, avDecH264)

	case strings.EqualFold(codecInfo.MimeType, source.MimeTypeVP8):
		if err := src.Element.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf(
				"application/x-rtp,media=video,payload=%d,encoding-name=VP8,clock-rate=%d",
				codecInfo.PayloadType, codecInfo.ClockRate,
			),
		)); err != nil {
			return err
		}

		rtpJitterBuffer, err := gst.NewElement("rtpjitterbuffer")
		if err != nil {
			return err
		}
		rtpJitterBuffer.SetArg("mode", "none")

		rtpVP8Depay, err := gst.NewElement("rtpvp8depay")
		if err != nil {
			return err
		}

		vp8Dec, err := gst.NewElement("vp8dec")
		if err != nil {
			return nil
		}

		b.videoElements = append(b.videoElements, rtpJitterBuffer, rtpVP8Depay, vp8Dec)

	default:
		return errors.ErrNotSupported(codecInfo.MimeType)
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
		fmt.Sprintf("video/x-raw,format=I420,width=%d,height=%d,framerate=%d/1", p.Width, p.Height, p.Framerate)),
	); err != nil {
		return err
	}

	b.videoElements = append(b.videoElements, videoConvert, videoScale, videoRate, decodedCaps)

	// Build encoding pipeline
	switch p.FileType {
	case livekit.EncodedFileType_MP4:
		var profile string
		switch p.VideoCodec {
		case livekit.VideoCodec_H264_BASELINE:
			profile = "baseline"
		case livekit.VideoCodec_H264_MAIN:
			profile = "main"
		case livekit.VideoCodec_H264_HIGH:
			profile = "high"
		default:
			return errors.ErrNotSupported(p.VideoCodec.String())
		}

		if err = b.buildH26XElements(264, profile, p); err != nil {
			return err
		}

	default:
		return errors.ErrNotSupported(p.FileType.String())
	}

	return nil
}

func (b *Bin) buildH26XElements(num int, profile string, p *params.Params) error {
	x26XEnc, err := gst.NewElement(fmt.Sprintf("x%denc", num))
	if err != nil {
		return err
	}
	if err = x26XEnc.SetProperty("bitrate", uint(p.VideoBitrate)); err != nil {
		return err
	}
	x26XEnc.SetArg("speed-preset", "veryfast")
	x26XEnc.SetArg("tune", "zerolatency")

	encodedCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return err
	}
	if err = encodedCaps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-h%d,profile=%s,framerate=%d/1", num, profile, p.Framerate),
	)); err != nil {
		return err
	}

	if num == 264 {
		b.videoElements = append(b.videoElements, x26XEnc, encodedCaps)
		return nil
	}

	h265parse, err := gst.NewElement("h265parse")
	if err != nil {
		return err
	}

	b.videoElements = append(b.videoElements, x26XEnc, encodedCaps, h265parse)
	return nil
}

func (b *Bin) buildVPXElements(num int, p *params.Params) error {
	vpXEnc, err := gst.NewElement(fmt.Sprintf("vp%denc", num))
	if err != nil {
		return err
	}

	encodedCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return err
	}
	if err = encodedCaps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-vp%d,framerate=%d/1", num, p.Framerate),
	)); err != nil {
		return err
	}

	b.videoElements = append(b.videoElements, vpXEnc, encodedCaps)
	return nil
}
