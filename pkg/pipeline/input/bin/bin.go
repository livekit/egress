package bin

import (
	"context"
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/tracer"
)

type InputBin struct {
	bin *gst.Bin

	audioElements []*gst.Element
	videoElements []*gst.Element
	multiQueue    *gst.Element
	mux           *gst.Element
	audioPad      *gst.Pad
	videoPad      *gst.Pad
}

func New(audioElements, videoElements []*gst.Element) *InputBin {
	return &InputBin{
		bin:           gst.NewBin("input"),
		audioElements: audioElements,
		videoElements: videoElements,
	}
}

func (b *InputBin) Build(ctx context.Context, p *params.Params) error {
	ctx, span := tracer.Start(ctx, "Input.build")
	defer span.End()

	var err error
	if err = b.bin.AddMany(b.audioElements...); err != nil {
		return err
	}
	if err = b.bin.AddMany(b.videoElements...); err != nil {
		return err
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
	b.mux, err = BuildMux(p)
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
	} else if len(b.audioElements) != 0 {
		b.audioPad = b.multiQueue.GetRequestPad("sink_%u")
		ghostPad = gst.NewGhostPad("src", b.multiQueue.GetStaticPad("src_0"))
	} else if len(b.videoElements) != 0 {
		b.videoPad = b.multiQueue.GetRequestPad("sink_%u")
		ghostPad = gst.NewGhostPad("src", b.multiQueue.GetStaticPad("src_0"))
	}

	// if b.mux != nil {
	// 	// For HLS, there will be no 'src' pad
	// 	pad := b.mux.GetStaticPad("src")
	// 	if pad != nil {
	// 		ghostPad = gst.NewGhostPad("src", pad)
	// 	}
	// } else if len(b.audioElements) > 0 {
	// 	last := b.audioElements[len(b.audioElements)-1]
	// 	ghostPad = gst.NewGhostPad("src", last.GetStaticPad("src"))
	// } else if len(b.videoElements) > 0 {
	// 	last := b.audioElements[len(b.videoElements)-1]
	// 	ghostPad = gst.NewGhostPad("src", last.GetStaticPad("src"))
	// }

	if ghostPad != nil && !b.bin.AddPad(ghostPad.Pad) {
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
	if len(b.audioElements) != 0 {
		if err := gst.ElementLinkMany(b.audioElements...); err != nil {
			return err
		}

		queuePad := b.audioPad
		if queuePad == nil {
			queuePad = b.multiQueue.GetRequestPad("sink_%u")
		}

		last := b.audioElements[len(b.audioElements)-1]
		if linkReturn := last.GetStaticPad("src").Link(queuePad); linkReturn != gst.PadLinkOK {
			return errors.ErrPadLinkFailed("audio queue", linkReturn.String())
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

			// if linkReturn := last.GetStaticPad("src").Link(muxAudioPad); linkReturn != gst.PadLinkOK {
			if linkReturn := b.multiQueue.GetStaticPad(fmt.Sprintf("src_%d", mqPad)).Link(muxAudioPad); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("audio mux", linkReturn.String())
			}
		}

		mqPad++
	}

	// link video elements
	if len(b.videoElements) != 0 {
		if err := gst.ElementLinkMany(b.videoElements...); err != nil {
			return err
		}

		queuePad := b.videoPad
		if queuePad == nil {
			queuePad = b.multiQueue.GetRequestPad("sink_%u")
		}

		last := b.videoElements[len(b.videoElements)-1]
		if linkReturn := last.GetStaticPad("src").Link(queuePad); linkReturn != gst.PadLinkOK {
			return errors.ErrPadLinkFailed("video queue", linkReturn.String())
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
			// if linkReturn := last.GetStaticPad("src").Link(muxVideoPad); linkReturn != gst.PadLinkOK {
			if linkReturn := b.multiQueue.GetStaticPad(fmt.Sprintf("src_%d", mqPad)).Link(muxVideoPad); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("video mux", linkReturn.String())
			}
		}
	}

	return nil
}
