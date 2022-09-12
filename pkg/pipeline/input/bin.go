package input

import (
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/source"
)

type Bin struct {
	source.Source

	bin           *gst.Bin
	audioElements []*gst.Element
	videoElements []*gst.Element

	multiQueue *gst.Element
	audioPad   *gst.Pad
	videoPad   *gst.Pad

	mux *gst.Element
}

func (b *Bin) Bin() *gst.Bin {
	return b.bin
}

func (b *Bin) Element() *gst.Element {
	return b.bin.Element
}

func (b *Bin) Link() error {
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
			if linkReturn := b.multiQueue.GetStaticPad(fmt.Sprintf("src_%d", mqPad)).Link(muxVideoPad); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("video mux", linkReturn.String())
			}
		}
	}

	return nil
}
