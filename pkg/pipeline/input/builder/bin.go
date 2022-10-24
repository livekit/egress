package builder

import (
	"context"
	"fmt"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/tracer"
)

const latency = uint64(41e8) // slightly larger than max audio latency

type InputBin struct {
	bin *gst.Bin

	audio    *AudioInput
	audioPad *gst.Pad

	video    *VideoInput
	videoPad *gst.Pad

	multiQueue *gst.Element
	mux        *gst.Element
}

func NewWebInput(ctx context.Context, p *params.Params) (*InputBin, error) {
	input := &InputBin{
		bin: gst.NewBin("input"),
	}

	if p.AudioEnabled {
		audio, err := NewWebAudioInput(p)
		if err != nil {
			return nil, err
		}
		input.audio = audio
	}

	if p.VideoEnabled {
		video, err := NewWebVideoInput(p)
		if err != nil {
			return nil, err
		}
		input.video = video
	}

	if err := input.build(ctx, p); err != nil {
		return nil, err
	}

	return input, nil
}

func NewSDKInput(ctx context.Context, p *params.Params, audioSrc, videoSrc *app.Source, audioCodec, videoCodec webrtc.RTPCodecParameters) (*InputBin, error) {
	input := &InputBin{
		bin: gst.NewBin("input"),
	}

	if p.AudioEnabled {
		audio, err := NewSDKAudioInput(p, audioSrc, audioCodec)
		if err != nil {
			return nil, err
		}
		input.audio = audio
	}

	if p.VideoEnabled {
		video, err := NewSDKVideoInput(p, videoSrc, videoCodec)
		if err != nil {
			return nil, err
		}
		input.video = video
	}

	if err := input.build(ctx, p); err != nil {
		return nil, err
	}

	return input, nil
}

func (b *InputBin) build(ctx context.Context, p *params.Params) error {
	ctx, span := tracer.Start(ctx, "Input.build")
	defer span.End()

	var err error
	// add audio to bin
	if b.audio != nil {
		if err = b.audio.AddToBin(b.bin); err != nil {
			return err
		}
	}

	// add video to bin
	if b.video != nil {
		if err = b.video.AddToBin(b.bin); err != nil {
			return err
		}
	}

	// queue
	b.multiQueue, err = gst.NewElement("multiqueue")
	if err != nil {
		return err
	}
	if err = b.bin.Add(b.multiQueue); err != nil {
		return err
	}

	// mux
	b.mux, err = buildMux(p)
	if err != nil {
		return err
	}
	if b.mux != nil {
		if err = b.bin.Add(b.mux); err != nil {
			return err
		}
	}

	// HLS has no output bin
	if p.OutputType == params.OutputTypeHLS {
		return nil
	}

	// create ghost pad
	var ghostPad *gst.GhostPad
	if b.mux != nil {
		ghostPad = gst.NewGhostPad("src", b.mux.GetStaticPad("src"))
	} else if b.audio != nil {
		b.audioPad = b.multiQueue.GetRequestPad("sink_%u")
		ghostPad = gst.NewGhostPad("src", b.multiQueue.GetStaticPad("src_0"))
	} else if b.video != nil {
		b.videoPad = b.multiQueue.GetRequestPad("sink_%u")
		ghostPad = gst.NewGhostPad("src", b.multiQueue.GetStaticPad("src_0"))
	}
	if ghostPad == nil || !b.bin.AddPad(ghostPad.Pad) {
		return errors.ErrGhostPadFailed
	}

	return nil
}

func (b *InputBin) Bin() *gst.Bin {
	return b.bin
}

func (b *InputBin) Element() *gst.Element {
	return b.bin.Element
}

func (b *InputBin) Link() error {
	mqPad := 0

	// link audio elements
	if b.audio != nil {
		if err := b.audio.Link(); err != nil {
			return err
		}

		queuePad := b.audioPad
		if queuePad == nil {
			queuePad = b.multiQueue.GetRequestPad("sink_%u")
		}

		if linkReturn := b.audio.GetSrcPad().Link(queuePad); linkReturn != gst.PadLinkOK {
			return errors.ErrPadLinkFailed("audio", "multiQueue", linkReturn.String())
		}

		if b.mux != nil {
			// Different muxers use different pad naming
			muxAudioPad := b.mux.GetRequestPad("audio")
			if muxAudioPad == nil {
				muxAudioPad = b.mux.GetRequestPad("audio_%u")
			}
			if muxAudioPad == nil {
				return errors.New("no audio pad found")
			}

			if linkReturn := b.multiQueue.GetStaticPad(fmt.Sprintf("src_%d", mqPad)).Link(muxAudioPad); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("audio", "mux", linkReturn.String())
			}
		}

		mqPad++
	}

	// link video elements
	if b.video != nil {
		if err := b.video.Link(); err != nil {
			return err
		}

		queuePad := b.videoPad
		if queuePad == nil {
			queuePad = b.multiQueue.GetRequestPad("sink_%u")
		}

		if linkReturn := b.video.GetSrcPad().Link(queuePad); linkReturn != gst.PadLinkOK {
			return errors.ErrPadLinkFailed("video", "multiQueue", linkReturn.String())
		}

		if b.mux != nil {
			// Different muxers use different pad naming
			muxVideoPad := b.mux.GetRequestPad("video")
			if muxVideoPad == nil {
				muxVideoPad = b.mux.GetRequestPad("video_%u")
			}
			if muxVideoPad == nil {
				return errors.New("no video pad found")
			}

			if linkReturn := b.multiQueue.GetStaticPad(fmt.Sprintf("src_%d", mqPad)).Link(muxVideoPad); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("video", "mux", linkReturn.String())
			}
		}
	}

	return nil
}

func buildQueue() (*gst.Element, error) {
	queue, err := gst.NewElement("queue")
	if err != nil {
		return nil, err
	}
	if err = queue.SetProperty("max-size-time", latency); err != nil {
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

func buildMux(p *params.Params) (*gst.Element, error) {
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

func getSrcPad(elements []*gst.Element) *gst.Pad {
	return elements[len(elements)-1].GetStaticPad("src")
}
