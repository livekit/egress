package builder

import (
	"context"
	"fmt"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/tracer"
)

const Latency = uint64(41e8) // slightly larger than max audio latency

type InputBin struct {
	bin *gst.Bin

	audio      *AudioInput
	audioPad   *gst.Pad
	audioQueue *gst.Element

	video      *VideoInput
	videoPad   *gst.Pad
	videoQueue *gst.Element

	mux *gst.Element
}

func NewWebInput(ctx context.Context, p *config.PipelineConfig) (*InputBin, error) {
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

func NewSDKInput(ctx context.Context, p *config.PipelineConfig, audioSrc, videoSrc *app.Source, audioCodec, videoCodec webrtc.RTPCodecParameters) (*InputBin, error) {
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

func (b *InputBin) build(ctx context.Context, p *config.PipelineConfig) error {
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

	// queues
	if b.audio != nil {
		b.audioQueue, err = buildQueue(Latency, false)
		if err != nil {
			return err
		}

		if err = b.bin.Add(b.audioQueue); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		b.audioPad = b.audioQueue.GetStaticPad("sink")
	}

	if b.video != nil {
		b.videoQueue, err = buildQueue(Latency, false)
		if err != nil {
			return err
		}

		if err = b.bin.Add(b.videoQueue); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		b.videoPad = b.videoQueue.GetStaticPad("sink")
	}

	// mux
	b.mux, err = buildMux(p)
	if err != nil {
		return err
	}
	if b.mux != nil {
		if err = b.bin.Add(b.mux); err != nil {
			return errors.ErrGstPipelineError(err)
		}
	}

	// HLS has no output bin
	if p.OutputType == types.OutputTypeHLS {
		return nil
	}

	// create ghost pad
	var ghostPad *gst.GhostPad
	if b.mux != nil {
		ghostPad = gst.NewGhostPad("src", b.mux.GetStaticPad("src"))
	} else if b.audio != nil {
		ghostPad = gst.NewGhostPad("src", b.audioQueue.GetStaticPad("src"))
	} else if b.video != nil {
		ghostPad = gst.NewGhostPad("src", b.videoQueue.GetStaticPad("src"))
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
	// link audio elements
	if b.audio != nil {
		if err := b.audio.Link(); err != nil {
			return err
		}

		if linkReturn := b.audio.GetSrcPad().Link(b.audioPad); linkReturn != gst.PadLinkOK {
			return errors.ErrPadLinkFailed("audio", "queue", linkReturn.String())
		}

		if b.mux != nil {
			// Different muxers use different pad naming
			muxAudioPad := b.mux.GetRequestPad("audio")
			if muxAudioPad == nil {
				muxAudioPad = b.mux.GetRequestPad("audio_%u")
			}
			if muxAudioPad == nil {
				return errors.ErrGstPipelineError(errors.New("no audio pad found"))
			}

			if linkReturn := b.audioQueue.GetStaticPad("src").Link(muxAudioPad); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("audio", "mux", linkReturn.String())
			}
		}
	}

	// link video elements
	if b.video != nil {
		if err := b.video.Link(); err != nil {
			return err
		}

		if linkReturn := b.video.GetSrcPad().Link(b.videoPad); linkReturn != gst.PadLinkOK {
			return errors.ErrPadLinkFailed("video", "queue", linkReturn.String())
		}

		if b.mux != nil {
			// Different muxers use different pad naming
			muxVideoPad := b.mux.GetRequestPad("video")
			if muxVideoPad == nil {
				muxVideoPad = b.mux.GetRequestPad("video_%u")
			}
			if muxVideoPad == nil {
				return errors.ErrGstPipelineError(errors.New("no video pad found"))
			}

			if linkReturn := b.videoQueue.GetStaticPad("src").Link(muxVideoPad); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("video", "mux", linkReturn.String())
			}
		}
	}

	return nil
}

func buildQueue(latency uint64, leaky bool) (*gst.Element, error) {
	queue, err := gst.NewElement("queue")
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = queue.SetProperty("max-size-time", latency); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = queue.SetProperty("max-size-bytes", uint(0)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = queue.SetProperty("max-size-buffers", uint(0)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if leaky {
		queue.SetArg("leaky", "downstream")
	}

	return queue, nil
}

func buildMux(p *config.PipelineConfig) (*gst.Element, error) {
	switch p.OutputType {
	case types.OutputTypeRaw:
		return nil, nil

	case types.OutputTypeOGG:
		return gst.NewElement("oggmux")

	case types.OutputTypeIVF:
		return gst.NewElement("avmux_ivf")

	case types.OutputTypeMP4:
		return gst.NewElement("mp4mux")

	case types.OutputTypeTS:
		return gst.NewElement("mpegtsmux")

	case types.OutputTypeWebM:
		return gst.NewElement("webmmux")

	case types.OutputTypeRTMP:
		mux, err := gst.NewElement("flvmux")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("streamable", true); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		// Increase the flv latency as video input is sometines late
		if err = mux.SetProperty("latency", uint64(1e8)); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}

		return mux, nil

	case types.OutputTypeHLS:
		mux, err := gst.NewElement("splitmuxsink")
		if err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("max-size-time", uint64(time.Duration(p.SegmentDuration)*time.Second)); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("send-keyframe-requests", true); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("async-finalize", true); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("muxer-factory", "mpegtsmux"); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		if err = mux.SetProperty("location", fmt.Sprintf("%s_%%05d.ts", p.LocalFilePrefix)); err != nil {
			return nil, errors.ErrGstPipelineError(err)
		}
		return mux, nil

	default:
		return nil, errors.ErrInvalidInput("output type")
	}
}

func getSrcPad(elements []*gst.Element) *gst.Pad {
	return elements[len(elements)-1].GetStaticPad("src")
}
