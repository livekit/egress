package bin

import (
	"fmt"
	"time"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
)

func BuildAudioEncoder(p *params.Params) ([]*gst.Element, error) {
	audioConvert, err := gst.NewElement("audioconvert")
	if err != nil {
		return nil, err
	}

	// TODO: sinc-filter-mode=full will use more memory but much less CPU
	audioResample, err := gst.NewElement("audioresample")
	if err != nil {
		return nil, err
	}

	var audioCaps string
	var encoder *gst.Element
	switch p.AudioCodec {
	case params.MimeTypeOpus:
		audioCaps = "audio/x-raw,format=S16LE,layout=interleaved,rate=48000,channels=2"
		encoder, err = gst.NewElement("opusenc")
		if err != nil {
			return nil, err
		}
		if err = encoder.SetProperty("bitrate", int(p.AudioBitrate*1000)); err != nil {
			return nil, err
		}

	case params.MimeTypeAAC:
		audioCaps = fmt.Sprintf("audio/x-raw,format=S16LE,layout=interleaved,rate=%d,channels=2", p.AudioFrequency)
		encoder, err = gst.NewElement("faac")
		if err != nil {
			return nil, err
		}
		if err = encoder.SetProperty("bitrate", int(p.AudioBitrate*1000)); err != nil {
			return nil, err
		}
	}

	audioCapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, err
	}
	if err = audioCapsFilter.SetProperty("caps", gst.NewCapsFromString(audioCaps)); err != nil {
		return nil, err
	}

	// audioQueue, err := BuildQueue()
	// if err != nil {
	// 	return nil, err
	// }

	return []*gst.Element{audioConvert, audioResample, audioCapsFilter, encoder}, nil
}

func BuildVideoEncoder(p *params.Params) ([]*gst.Element, error) {
	switch p.VideoCodec {
	// we only encode h264, the rest are too slow
	case params.MimeTypeH264:
		x264Enc, err := gst.NewElement("x264enc")
		if err != nil {
			return nil, err
		}
		if err = x264Enc.SetProperty("bitrate", uint(p.VideoBitrate)); err != nil {
			return nil, err
		}
		x264Enc.SetArg("speed-preset", "veryfast")
		if p.OutputType == params.OutputTypeHLS {
			if err = x264Enc.SetProperty("key-int-max", uint(int32(p.SegmentDuration)*p.Framerate)); err != nil {
				return nil, err
			}
			// Avoid key frames other than at segments boundaries as splitmuxsink can become inconsistent otherwise
			if err = x264Enc.SetProperty("option-string", "scenecut=0"); err != nil {
				return nil, err
			}
		}

		if p.VideoProfile == "" {
			p.VideoProfile = params.ProfileMain
		}

		encodedCaps, err := gst.NewElement("capsfilter")
		if err != nil {
			return nil, err
		}

		if err = encodedCaps.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf("video/x-h264,profile=%s,framerate=%d/1", p.VideoProfile, p.Framerate),
		)); err != nil {
			return nil, err
		}

		// videoQueue, err := BuildQueue()
		// if err != nil {
		// 	return nil, err
		// }

		return []*gst.Element{x264Enc, encodedCaps}, nil

	default:
		return nil, errors.ErrNotSupported(fmt.Sprintf("%s encoding", p.VideoCodec))
	}
}

func BuildQueue() (*gst.Element, error) {
	queue, err := gst.NewElement("queue")
	if err != nil {
		return nil, err
	}
	if err = queue.SetProperty("max-size-time", uint64(3e9)); err != nil {
		return nil, err
	}
	if err = queue.SetProperty("max-size-bytes", uint(0)); err != nil {
		return nil, err
	}
	if err = queue.SetProperty("max-size-buffers", uint(0)); err != nil {
		return nil, err
	}
	return queue, nil
}

func BuildMux(p *params.Params) (*gst.Element, error) {
	switch p.OutputType {
	case params.OutputTypeRaw:
		return nil, nil

	case params.OutputTypeOGG:
		return gst.NewElement("oggmux")

	case params.OutputTypeIVF:
		return gst.NewElement("avmux_ivf")

	case params.OutputTypeMP4:
		return gst.NewElement("mp4mux")

	case params.OutputTypeTS:
		return gst.NewElement("mpegtsmux")

	case params.OutputTypeWebM:
		return gst.NewElement("webmmux")

	case params.OutputTypeRTMP:
		mux, err := gst.NewElement("flvmux")
		if err != nil {
			return nil, err
		}
		if err = mux.SetProperty("streamable", true); err != nil {
			return nil, err
		}
		// if err = mux.SetProperty("latency", uint64(200000000)); err != nil {
		// 	return nil, err
		// }
		return mux, nil

	case params.OutputTypeHLS:
		mux, err := gst.NewElement("splitmuxsink")
		if err != nil {
			return nil, err
		}
		if err = mux.SetProperty("max-size-time", uint64(time.Duration(p.SegmentDuration)*time.Second)); err != nil {
			return nil, err
		}
		if err = mux.SetProperty("async-finalize", true); err != nil {
			return nil, err
		}
		if err = mux.SetProperty("muxer-factory", "mpegtsmux"); err != nil {
			return nil, err
		}
		if err = mux.SetProperty("location", fmt.Sprintf("%s_%%05d.ts", p.LocalFilePrefix)); err != nil {
			return nil, err
		}
		return mux, nil

	default:
		return nil, errors.ErrInvalidInput("output type")
	}
}
