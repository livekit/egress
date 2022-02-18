package input

import (
	"fmt"

	"github.com/livekit/protocol/livekit"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/errors"
)

type Bin interface {
	LinkElements() error
	Bin() *gst.Bin
	Source
}

type Source interface {
	RoomStarted() chan struct{}
	RoomEnded() chan struct{}
	Close()
}

type inputBin struct {
	Source

	bin           *gst.Bin
	audioElements []*gst.Element
	videoElements []*gst.Element
	audioQueue    *gst.Element
	videoQueue    *gst.Element
	mux           *gst.Element

	isStream bool
}

func New(conf *config.Config, params *config.Params) (Bin, error) {
	// input source
	var source Source
	var err error
	if params.IsWebInput {
		source, err = newWebSource(conf, params)
	} else {
		source, err = newSDKSource()
	}
	if err != nil {
		return nil, err
	}

	var elements []*gst.Element

	// audio elements
	audioElements, audioQueue, err := buildAudioElements(params)
	if err != nil {
		return nil, err
	}
	if audioElements != nil {
		elements = append(elements, audioElements...)
	}

	// video elements
	videoElements, videoQueue, err := buildVideoElements(params)
	if err != nil {
		return nil, err
	}
	if videoElements != nil {
		elements = append(elements, videoElements...)
	}

	// mux
	mux, err := buildMux(params)
	if err != nil {
		return nil, err
	}
	elements = append(elements, mux)

	// create bin
	bin := gst.NewBin("input")
	err = bin.AddMany(elements...)
	if err != nil {
		return nil, err
	}

	// create ghost pad
	ghostPad := gst.NewGhostPad("src", mux.GetStaticPad("src"))
	if !bin.AddPad(ghostPad.Pad) {
		return nil, errors.ErrGhostPadFailed
	}

	return &inputBin{
		Source:        source,
		bin:           bin,
		audioElements: audioElements,
		audioQueue:    audioQueue,
		videoElements: videoElements,
		videoQueue:    videoQueue,
		mux:           mux,
		isStream:      params.IsStream,
	}, nil
}

func (b *inputBin) LinkElements() error {
	// link audio elements
	if b.audioQueue != nil {
		if err := gst.ElementLinkMany(b.audioElements...); err != nil {
			return err
		}

		var muxAudioPad *gst.Pad
		if b.isStream {
			muxAudioPad = b.mux.GetRequestPad("audio")
		} else {
			muxAudioPad = b.mux.GetRequestPad("audio_%u")
		}

		if linkReturn := b.audioQueue.GetStaticPad("src").Link(muxAudioPad); linkReturn != gst.PadLinkOK {
			return fmt.Errorf("audio mux pad link failed: %s", linkReturn.String())
		}
	}

	// link video elements
	if b.videoQueue != nil {
		if err := gst.ElementLinkMany(b.videoElements...); err != nil {
			return err
		}

		var muxVideoPad *gst.Pad
		if b.isStream {
			muxVideoPad = b.mux.GetRequestPad("video")
		} else {
			muxVideoPad = b.mux.GetRequestPad("video_%u")
		}

		if linkReturn := b.videoQueue.GetStaticPad("src").Link(muxVideoPad); linkReturn != gst.PadLinkOK {
			return fmt.Errorf("video mux pad link failed: %s", linkReturn.String())
		}
	}

	return nil
}

func (b *inputBin) Bin() *gst.Bin {
	return b.bin
}

func buildAudioElements(params *config.Params) ([]*gst.Element, *gst.Element, error) {
	if !params.AudioEnabled {
		return nil, nil, nil
	}

	var audioElements []*gst.Element
	if params.IsWebInput {
		pulseSrc, err := gst.NewElement("pulsesrc")
		if err != nil {
			return nil, nil, err
		}

		audioConvert, err := gst.NewElement("audioconvert")
		if err != nil {
			return nil, nil, err
		}

		audioElements = append(audioElements, pulseSrc, audioConvert)
	} else {
		return nil, nil, errors.ErrNotSupported("sdk input")
	}

	switch params.AudioCodec {
	case livekit.AudioCodec_OPUS:
		audioCapsFilter, err := gst.NewElement("capsfilter")
		if err != nil {
			return nil, nil, err
		}
		// TODO: opus audio ends up mono even with channels=2
		err = audioCapsFilter.SetProperty("caps", gst.NewCapsFromString(
			"audio/x-raw,format=S16LE,layout=interleaved,rate=48000,channels=2",
		))
		if err != nil {
			return nil, nil, err
		}

		opus, err := gst.NewElement("opusenc")
		if err != nil {
			return nil, nil, err
		}
		if err = opus.SetProperty("bitrate", int(params.AudioBitrate*1000)); err != nil {
			return nil, nil, err
		}

		audioElements = append(audioElements, opus)

	case livekit.AudioCodec_AAC:
		audioCapsFilter, err := gst.NewElement("capsfilter")
		if err != nil {
			return nil, nil, err
		}
		err = audioCapsFilter.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf("audio/x-raw,format=S16LE,layout=interleaved,rate=%d,channels=2", params.AudioFrequency),
		))
		if err != nil {
			return nil, nil, err
		}

		faac, err := gst.NewElement("faac")
		if err != nil {
			return nil, nil, err
		}
		if err = faac.SetProperty("bitrate", int(params.AudioBitrate*1000)); err != nil {
			return nil, nil, err
		}
		audioElements = append(audioElements, audioCapsFilter, faac)
	}

	audioQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, nil, err
	}
	if err = audioQueue.SetProperty("max-size-time", uint64(3e9)); err != nil {
		return nil, nil, err
	}
	audioElements = append(audioElements, audioQueue)
	return audioElements, audioQueue, nil
}

func buildVideoElements(params *config.Params) ([]*gst.Element, *gst.Element, error) {
	if !params.VideoEnabled {
		return nil, nil, nil
	}

	var videoElements []*gst.Element
	if params.IsWebInput {
		xImageSrc, err := gst.NewElement("ximagesrc")
		if err != nil {
			return nil, nil, err
		}
		err = xImageSrc.SetProperty("use-damage", false)
		if err != nil {
			return nil, nil, err
		}
		err = xImageSrc.SetProperty("show-pointer", false)
		if err != nil {
			return nil, nil, err
		}

		videoConvert, err := gst.NewElement("videoconvert")
		if err != nil {
			return nil, nil, err
		}

		videoFramerateCaps, err := gst.NewElement("capsfilter")
		if err != nil {
			return nil, nil, err
		}
		err = videoFramerateCaps.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf("video/x-raw,framerate=%d/1", params.Framerate),
		))
		if err != nil {
			return nil, nil, err
		}

		videoElements = append(videoElements, xImageSrc, videoConvert, videoFramerateCaps)
	} else {
		return nil, nil, errors.ErrNotSupported("sdk input")
	}

	var encodingElements []*gst.Element
	var err error
	switch params.VideoCodec {
	case livekit.VideoCodec_H264_BASELINE:
		encodingElements, err = buildH26XElements(264, "baseline", params)
	case livekit.VideoCodec_H264_MAIN:
		encodingElements, err = buildH26XElements(264, "main", params)
	case livekit.VideoCodec_H264_HIGH:
		encodingElements, err = buildH26XElements(264, "high", params)
	// case livekit.VideoCodec_VP8:
	//  // TODO: vp8 low quality/choppy
	// 	encodingElements, err = buildVPXElements(8, params)
	// case livekit.VideoCodec_VP9:
	//  // TODO: vp9 is extremely slow, audio gets dropped, default parameters cannot keep up with live source
	// 	encodingElements, err = buildVPXElements(9, params)
	// case livekit.VideoCodec_HEVC_MAIN:
	//  // TODO: hevc low quality/choppy
	// 	encodingElements, err = buildH26XElements(265, "main", params)
	// case livekit.VideoCodec_HEVC_HIGH:
	//  // TODO: hevc low quality/choppy
	// 	encodingElements, err = buildH26XElements(265, "main", params)
	default:
		return nil, nil, errors.ErrNotSupported(params.VideoCodec.String())
	}
	if err != nil {
		return nil, nil, err
	}
	videoElements = append(videoElements, encodingElements...)

	videoQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, nil, err
	}
	if err = videoQueue.SetProperty("max-size-time", uint64(3e9)); err != nil {
		return nil, nil, err
	}
	videoElements = append(videoElements, videoQueue)
	return videoElements, videoQueue, nil
}

func buildH26XElements(num int, profile string, params *config.Params) ([]*gst.Element, error) {
	x26XEnc, err := gst.NewElement(fmt.Sprintf("x%denc", num))
	if err != nil {
		return nil, err
	}
	if err = x26XEnc.SetProperty("bitrate", uint(params.VideoBitrate)); err != nil {
		return nil, err
	}
	x26XEnc.SetArg("speed-preset", "veryfast")
	x26XEnc.SetArg("tune", "zerolatency")

	videoProfileCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, err
	}
	if err = videoProfileCaps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-h%d,profile=%s,framerate=%d/1", num, profile, params.Framerate),
	)); err != nil {
		return nil, err
	}

	if num == 264 {
		return []*gst.Element{x26XEnc, videoProfileCaps}, nil
	}

	h265parse, err := gst.NewElement("h265parse")
	if err != nil {
		return nil, err
	}
	return []*gst.Element{x26XEnc, videoProfileCaps, h265parse}, nil
}

func buildVPXElements(num int, params *config.Params) ([]*gst.Element, error) {
	vpXEnc, err := gst.NewElement(fmt.Sprintf("vp%denc", num))
	if err != nil {
		return nil, err
	}

	videoProfileCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, err
	}
	err = videoProfileCaps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-vp%d,framerate=%d/1", num, params.Framerate),
	))
	if err != nil {
		return nil, err
	}

	return []*gst.Element{vpXEnc, videoProfileCaps}, nil
}

func buildMux(params *config.Params) (mux *gst.Element, err error) {
	if params.IsStream {
		switch params.StreamProtocol {
		case livekit.StreamProtocol_RTMP:
			mux, err = gst.NewElement("flvmux")
			if err != nil {
				return
			}
			err = mux.Set("streamable", true)
		case livekit.StreamProtocol_SRT:
			err = errors.ErrNotSupported("srt output")
		}
	} else {
		switch params.FileType {

		case livekit.EncodedFileType_MP4:
			mux, err = gst.NewElement("mp4mux")
			if err != nil {
				return
			}
			err = mux.SetProperty("faststart", true)
		case livekit.EncodedFileType_WEBM:
			mux, err = gst.NewElement("webmmux")
		case livekit.EncodedFileType_OGG:
			mux, err = gst.NewElement("oggmux")
		}
	}

	return
}
