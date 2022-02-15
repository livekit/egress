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

	// audio elements
	var audioElements []*gst.Element
	var audioQueue *gst.Element
	if params.AudioEnabled {
		if params.IsWebInput {
			pulseSrc, err := gst.NewElement("pulsesrc")
			if err != nil {
				return nil, err
			}

			audioConvert, err := gst.NewElement("audioconvert")
			if err != nil {
				return nil, err
			}

			audioCapsFilter, err := gst.NewElement("capsfilter")
			if err != nil {
				return nil, err
			}
			err = audioCapsFilter.SetProperty("caps", gst.NewCapsFromString(
				fmt.Sprintf("audio/x-raw,format=S16LE,layout=interleaved,rate=%d,channels=2", params.AudioFrequency),
			))
			if err != nil {
				return nil, err
			}

			audioElements = append(audioElements, pulseSrc, audioConvert, audioCapsFilter)
		} else {
			return nil, errors.ErrNotSupported("sdk input")
		}

		switch params.AudioCodec {
		case livekit.AudioCodec_OPUS:
			return nil, errors.ErrNotSupported("opus encoding")
		case livekit.AudioCodec_AAC:
			if params.FileType != livekit.EncodedFileType_MP4 {
				return nil, errors.ErrIncompatible(params.FileType, params.AudioCodec)
			}

			faac, err := gst.NewElement("faac")
			if err != nil {
				return nil, err
			}
			err = faac.SetProperty("bitrate", int(params.AudioBitrate*1000))
			if err != nil {
				return nil, err
			}

			audioQueue, err = gst.NewElement("queue")
			if err != nil {
				return nil, err
			}
			if err = audioQueue.SetProperty("max-size-time", uint64(3e9)); err != nil {
				return nil, err
			}

			audioElements = append(audioElements, faac, audioQueue)
		}
	}

	// video elements
	var videoElements []*gst.Element
	var videoQueue *gst.Element
	if params.VideoEnabled {
		if params.IsWebInput {
			xImageSrc, err := gst.NewElement("ximagesrc")
			if err != nil {
				return nil, err
			}
			err = xImageSrc.SetProperty("use-damage", false)
			if err != nil {
				return nil, err
			}
			err = xImageSrc.SetProperty("show-pointer", false)
			if err != nil {
				return nil, err
			}

			videoConvert, err := gst.NewElement("videoconvert")
			if err != nil {
				return nil, err
			}

			videoFramerateCaps, err := gst.NewElement("capsfilter")
			if err != nil {
				return nil, err
			}
			err = videoFramerateCaps.SetProperty("caps", gst.NewCapsFromString(
				fmt.Sprintf("video/x-raw,framerate=%d/1", params.Framerate),
			))
			if err != nil {
				return nil, err
			}

			videoElements = append(videoElements, xImageSrc, videoConvert, videoFramerateCaps)
		} else {
			return nil, errors.ErrNotSupported("sdk input")
		}

		var encodingElements []*gst.Element
		switch params.VideoCodec {
		case livekit.VideoCodec_H264_BASELINE:
			encodingElements, err = buildH264Elements("baseline", params)
		case livekit.VideoCodec_H264_MAIN:
			encodingElements, err = buildH264Elements("main", params)
		case livekit.VideoCodec_H264_HIGH:
			encodingElements, err = buildH264Elements("high", params)
		case livekit.VideoCodec_VP8:
			err = errors.ErrNotSupported("vp8 encoding")
		case livekit.VideoCodec_VP9:
			err = errors.ErrNotSupported("vp9 encoding")
		case livekit.VideoCodec_HEVC_MAIN:
			encodingElements, err = buildHEVCElements("main", params)
		case livekit.VideoCodec_HEVC_HIGH:
			encodingElements, err = buildHEVCElements("high", params)
		}
		if err != nil {
			return nil, err
		}

		videoQueue, err = gst.NewElement("queue")
		if err != nil {
			return nil, err
		}
		if err = videoQueue.SetProperty("max-size-time", uint64(3e9)); err != nil {
			return nil, err
		}
		encodingElements = append(encodingElements, videoQueue)
		videoElements = append(videoElements, encodingElements...)
	}

	// mux
	var mux *gst.Element
	if params.IsStream {
		switch params.StreamProtocol {
		case livekit.StreamProtocol_RTMP:
			mux, err = gst.NewElement("flvmux")
			if err != nil {
				return nil, err
			}
			err = mux.Set("streamable", true)
			if err != nil {
				return nil, err
			}
		case livekit.StreamProtocol_SRT:
			return nil, errors.ErrNotSupported("srt output")
		}
	} else {
		switch params.FileType {
		case livekit.EncodedFileType_MP4:
			mux, err = gst.NewElement("mp4mux")
			if err != nil {
				return nil, err
			}
			err = mux.SetProperty("faststart", true)
			if err != nil {
				return nil, err
			}
		case livekit.EncodedFileType_WEBM:
			return nil, errors.ErrNotSupported("webm encoding")
		case livekit.EncodedFileType_OGG:
			return nil, errors.ErrNotSupported("ogg encoding")
		}
	}

	// create bin
	bin := gst.NewBin("input")
	allElements := append(audioElements, videoElements...)
	allElements = append(allElements, mux)
	err = bin.AddMany(allElements...)
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

func buildH264Elements(profile string, params *config.Params) ([]*gst.Element, error) {
	x264Enc, err := gst.NewElement("x264enc")
	if err != nil {
		return nil, err
	}
	if err = x264Enc.SetProperty("bitrate", uint(params.VideoBitrate)); err != nil {
		return nil, err
	}
	x264Enc.SetArg("speed-preset", "veryfast")
	x264Enc.SetArg("tune", "zerolatency")

	videoProfileCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, err
	}
	err = videoProfileCaps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-h264,profile=%s,framerate=%d/1", profile, params.Framerate),
	))
	if err != nil {
		return nil, err
	}

	return []*gst.Element{x264Enc, videoProfileCaps}, nil
}

func buildHEVCElements(profile string, params *config.Params) ([]*gst.Element, error) {
	return nil, errors.ErrNotSupported("hevc encoding")
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
			return fmt.Errorf("pad link: %s", linkReturn.String())
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
			return fmt.Errorf("pad link: %s", linkReturn.String())
		}
	}

	return nil
}

func (b *inputBin) Bin() *gst.Bin {
	return b.bin
}
