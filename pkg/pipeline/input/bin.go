package input

import (
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/livekit-egress/pkg/errors"
)

type Source interface {
	Playing(name string)
	StartRecording() chan struct{}
	EndRecording() chan struct{}
	Close()
}

type Bin struct {
	Source

	bin *gst.Bin

	audioElements []*gst.Element
	audioQueue    *gst.Element

	videoElements []*gst.Element
	videoQueue    *gst.Element

	mux *gst.Element

	isStream bool
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

		var muxAudioPad *gst.Pad
		if b.isStream {
			muxAudioPad = b.mux.GetRequestPad("audio")
		} else {
			muxAudioPad = b.mux.GetRequestPad("audio_%u")
		}

		if linkReturn := b.audioQueue.GetStaticPad("src").Link(muxAudioPad); linkReturn != gst.PadLinkOK {
			return errors.ErrPadLinkFailed("audio mux", linkReturn.String())
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
			return errors.ErrPadLinkFailed("video mux", linkReturn.String())
		}
	}

	return nil
}
