package input

import (
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/source"
)

type Bin struct {
	source.Source

	bin *gst.Bin

	audioElements []*gst.Element
	audioQueue    *gst.Element

	videoElements []*gst.Element
	videoQueue    *gst.Element

	mux *gst.Element
}

func (b *Bin) Bin() *gst.Bin {
	return b.bin
}

func (b *Bin) Element() *gst.Element {
	return b.bin.Element
}

func (b *Bin) Link() error {
	// link audio elements
	if b.audioQueue != nil {
		if err := gst.ElementLinkMany(b.audioElements...); err != nil {
			return err
		}

		if b.mux != nil {
			// Different muxers use different pad naming
			muxAudioPad := b.mux.GetRequestPad("audio")
			if muxAudioPad == nil {
				muxAudioPad = b.mux.GetRequestPad("audio_%u")
			}
			if muxAudioPad == nil {
				return fmt.Errorf("No audio pad found")
			}

			if linkReturn := b.audioQueue.GetStaticPad("src").Link(muxAudioPad); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("audio mux", linkReturn.String())
			}
		}
	}

	// link video elements
	if b.videoQueue != nil {
		if err := gst.ElementLinkMany(b.videoElements...); err != nil {
			return err
		}

		if b.mux != nil {
			// Different muxers use different pad naming
			muxVideoPad := b.mux.GetRequestPad("video")
			if muxVideoPad == nil {
				muxVideoPad = b.mux.GetRequestPad("video_%u")
			}
			if muxVideoPad == nil {
				return fmt.Errorf("No video pad found")
			}
			if linkReturn := b.videoQueue.GetStaticPad("src").Link(muxVideoPad); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("video mux", linkReturn.String())
			}
		}
	}

	return nil
}
