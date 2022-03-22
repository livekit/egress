package composite

import (
	"fmt"
	"strings"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-egress/pkg/config"
	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/livekit-egress/pkg/source"
)

type Source interface {
	StartRecording() chan struct{}
	EndRecording() chan struct{}
	Close()
}

type inputBin struct {
	Source

	bin *gst.Bin

	audioSrc      *app.Source
	audioElements []*gst.Element
	audioQueue    *gst.Element

	videoSrc      *app.Source
	videoElements []*gst.Element
	videoQueue    *gst.Element

	mux *gst.Element
}

func newInputBin(conf *config.Config, p *params.Params) (*inputBin, error) {
	b := &inputBin{
		bin: gst.NewBin("input"),
	}

	// audio elements
	err := b.buildAudioElements(p)
	if err != nil {
		return nil, err
	}

	// video elements
	err = b.buildVideoElements(p)
	if err != nil {
		return nil, err
	}

	// source
	err = b.buildSource(conf, p)
	if err != nil {
		return nil, err
	}

	// mux
	err = b.buildMux(p)
	if err != nil {
		return nil, err
	}

	// create ghost pad
	ghostPad := gst.NewGhostPad("src", b.mux.GetStaticPad("src"))
	if !b.bin.AddPad(ghostPad.Pad) {
		return nil, errors.ErrGhostPadFailed
	}

	return b, nil
}

func (b *inputBin) buildAudioElements(p *params.Params) error {
	if !p.AudioEnabled {
		return nil
	}

	var err error
	if p.IsWebInput {
		pulseSrc, err := gst.NewElement("pulsesrc")
		if err != nil {
			return err
		}
		if err = pulseSrc.SetProperty("device", fmt.Sprintf("%s.monitor", p.Info.EgressId)); err != nil {
			return err
		}

		audioConvert, err := gst.NewElement("audioconvert")
		if err != nil {
			return err
		}

		b.audioElements = append(b.audioElements, pulseSrc, audioConvert)

		switch p.AudioCodec {
		case livekit.AudioCodec_OPUS:
			audioCapsFilter, err := gst.NewElement("capsfilter")
			if err != nil {
				return err
			}
			if err = audioCapsFilter.SetProperty("caps", gst.NewCapsFromString(
				"audio/x-raw,format=S16LE,layout=interleaved,rate=48000,channels=2",
			)); err != nil {
				return err
			}

			opus, err := gst.NewElement("opusenc")
			if err != nil {
				return err
			}
			if err = opus.SetProperty("bitrate", int(p.AudioBitrate*1000)); err != nil {
				return err
			}

			b.audioElements = append(b.audioElements, audioCapsFilter, opus)

		case livekit.AudioCodec_AAC:
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

			b.audioElements = append(b.audioElements, audioCapsFilter, faac)
		}

	} else {
		b.audioSrc, err = app.NewAppSrc()
		if err != nil {
			return err
		}

		b.audioElements = append(b.audioElements, b.audioSrc.Element)
	}

	b.audioQueue, err = gst.NewElement("queue")
	if err != nil {
		return err
	}
	if err = b.audioQueue.SetProperty("max-size-time", uint64(3e9)); err != nil {
		return err
	}
	b.audioElements = append(b.audioElements, b.audioQueue)
	return b.bin.AddMany(b.audioElements...)
}

func (b *inputBin) buildVideoElements(p *params.Params) error {
	if !p.VideoEnabled {
		return nil
	}

	var err error
	if p.IsWebInput {
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
		if err != nil {
			return err
		}
	} else {
		b.videoSrc, err = app.NewAppSrc()
		if err != nil {
			return err
		}

		b.videoElements = append(b.videoElements, b.videoSrc.Element)
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

func (b *inputBin) buildH26XElements(num int, profile string, p *params.Params) error {
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

func (b *inputBin) buildVPXElements(num int, p *params.Params) error {
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

func (b *inputBin) buildSource(conf *config.Config, p *params.Params) error {
	var err error
	if p.IsWebInput {
		b.Source, err = source.NewWebSource(conf, p)
	} else {
		b.Source, err = source.NewSDKSource(p, func(track *webrtc.TrackRemote) (media.Writer, error) {
			switch {
			case strings.EqualFold(track.Codec().MimeType, "audio/opus"):
				return &appWriter{src: b.audioSrc}, nil
			case strings.EqualFold(track.Codec().MimeType, "video/vp8"),
				strings.EqualFold(track.Codec().MimeType, "video/h264"):
				return &appWriter{src: b.videoSrc}, nil
			default:
				return nil, errors.ErrNotSupported(track.Codec().MimeType)
			}
		})
	}

	return err
}

type appWriter struct {
	src *app.Source
}

func (w *appWriter) WriteRTP(pkt *rtp.Packet) error {
	b, err := pkt.Marshal()
	if err != nil {
		return err
	}
	w.src.PushBuffer(gst.NewBufferFromBytes(b))
	return nil
}

func (w *appWriter) Close() error {
	return nil
}

func (b *inputBin) buildMux(p *params.Params) error {
	var err error
	if p.IsStream {
		switch p.StreamProtocol {
		case livekit.StreamProtocol_RTMP:
			b.mux, err = gst.NewElement("flvmux")
			if err != nil {
				return err
			}
			err = b.mux.Set("streamable", true)
			// case livekit.StreamProtocol_SRT:
			// 	err = errors.ErrNotSupported("srt output")
		}
	} else {
		switch p.FileType {

		case livekit.EncodedFileType_MP4:
			b.mux, err = gst.NewElement("mp4mux")
			if err != nil {
				return err
			}
			err = b.mux.SetProperty("faststart", true)
		// case livekit.EncodedFileType_WEBM:
		// 	mux, err = gst.NewElement("webmmux")
		case livekit.EncodedFileType_OGG:
			b.mux, err = gst.NewElement("oggmux")
		}
	}

	return b.bin.Add(b.mux)
}
