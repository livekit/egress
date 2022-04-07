package input

import (
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/livekit-egress/pkg/pipeline/source"
	"github.com/livekit/protocol/livekit"
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

	mimeType := <-p.VideoMimeType
	switch mimeType {
	case source.MimeTypeH264:
		if err := b.videoSrc.Element.SetProperty("caps", gst.NewCapsFromString(
			// TODO
			"application/x-rtp,media=video,payload=111,encoding-name=OPUS,clock-rate=48000",
		)); err != nil {
			return err
		}

		rtpJitterBuffer, err := gst.NewElement("rtpjitterbuffer")
		if err != nil {
			return err
		}

		rtpH264Depay, err := gst.NewElement("rtph264depay")
		if err != nil {
			return err
		}

		b.videoElements = append(b.videoElements, b.videoSrc.Element, rtpJitterBuffer, rtpH264Depay)

	case source.MimeTypeVP8:
		if err := b.videoSrc.Element.SetProperty("caps", gst.NewCapsFromString(
			// TODO
			"application/x-rtp,media=video,payload=111,encoding-name=OPUS,clock-rate=48000",
		)); err != nil {
			return err
		}

		rtpJitterBuffer, err := gst.NewElement("rtpjitterbuffer")
		if err != nil {
			return err
		}

		rtpOpusDepay, err := gst.NewElement("rtpopusdepay")
		if err != nil {
			return err
		}

		b.videoElements = append(b.videoElements, b.videoSrc.Element, rtpJitterBuffer, rtpOpusDepay)

	default:
		return errors.ErrNotSupported(mimeType)
	}

	switch p.VideoCodec {
	case livekit.VideoCodec_H264_BASELINE,
		livekit.VideoCodec_H264_MAIN,
		livekit.VideoCodec_H264_HIGH:

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
