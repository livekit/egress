package input

import (
	"fmt"

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
	b.videoSrc.SetDoTimestamp(true)
	b.videoSrc.SetFormat(gst.FormatTime)
	b.videoSrc.SetLive(true)

	var capsStr string
	var depay *gst.Element
	var err error

	mimeType := <-p.VideoMimeType
	switch mimeType {
	case source.MimeTypeH264:
		capsStr = "application/x-rtp,media=video,payload=96,clock-rate=90000,encoding-name=H264"
		depay, err = gst.NewElement("rtph264depay")

	case source.MimeTypeVP8:
		capsStr = "application/x-rtp,media=video,payload=96,clock-rate=90000,encoding-name=VP8"
		depay, err = gst.NewElement("rtpvp8depay")

	default:
		return errors.ErrNotSupported(mimeType)
	}

	if err = b.videoSrc.Element.SetProperty("caps", gst.NewCapsFromString(capsStr)); err != nil {
		return err
	}

	b.videoElements = append(b.videoElements, b.videoSrc.Element, depay)

	switch mimeType {
	case source.MimeTypeH264:

	case source.MimeTypeVP8:
		vp8Dec, err := gst.NewElement("vp8dec")
		if err != nil {
			return err
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

		videoRawCaps, err := gst.NewElement("capsfilter")
		if err != nil {
			return err
		}
		if err = videoRawCaps.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf("video/x-raw,format=I420,width=%d,height=%d,framerate=%d/1", p.Width, p.Height, p.Framerate),
		)); err != nil {
			return err
		}

		b.videoElements = append(b.videoElements, vp8Dec, videoConvert, videoScale, videoRate, videoRawCaps)

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

	videoProfileCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return err
	}
	if err = videoProfileCaps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-h%d,profile=%s,framerate=%d/1", num, profile, p.Framerate),
	)); err != nil {
		return err
	}

	if num == 264 {
		b.videoElements = append(b.videoElements, x26XEnc, videoProfileCaps)
		return nil
	}

	h265parse, err := gst.NewElement("h265parse")
	if err != nil {
		return err
	}

	b.videoElements = append(b.videoElements, x26XEnc, videoProfileCaps, h265parse)
	return nil
}

func (b *Bin) buildVPXElements(num int, p *params.Params) error {
	vpXEnc, err := gst.NewElement(fmt.Sprintf("vp%denc", num))
	if err != nil {
		return err
	}

	videoProfileCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return err
	}
	if err = videoProfileCaps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-vp%d,framerate=%d/1", num, p.Framerate),
	)); err != nil {
		return err
	}

	b.videoElements = append(b.videoElements, vpXEnc, videoProfileCaps)
	return nil
}
