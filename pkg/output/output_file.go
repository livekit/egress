package output

import (
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/livekit-egress/pkg/errors"
)

type fileOutputBin struct {
	bin  *gst.Bin
	sink *gst.Element
}

func newFileOutputBin(filename string) (Bin, error) {
	// create elements
	sink, err := gst.NewElement("filesink")
	if err != nil {
		return nil, err
	}
	if err = sink.SetProperty("location", filename); err != nil {
		return nil, err
	}
	if err = sink.SetProperty("sync", false); err != nil {
		return nil, err
	}

	// create bin
	bin := gst.NewBin("output")
	if err = bin.Add(sink); err != nil {
		return nil, err
	}

	// add ghost pad
	ghostPad := gst.NewGhostPad("sink", sink.GetStaticPad("sink"))
	if !bin.AddPad(ghostPad.Pad) {
		return nil, errors.ErrGhostPadFailed
	}

	return &fileOutputBin{
		bin:  bin,
		sink: sink,
	}, nil
}

func (b *fileOutputBin) LinkElements() error {
	return nil
}

func (b *fileOutputBin) Bin() *gst.Bin {
	return b.bin
}

func (b *fileOutputBin) AddSink(url string) error {
	return errors.ErrCannotMixEgressTypes
}

func (b *fileOutputBin) RemoveSink(url string) error {
	return errors.ErrCannotMixEgressTypes
}

func (b *fileOutputBin) RemoveSinkByName(name string) error {
	return errors.ErrCannotMixEgressTypes
}

func (b *fileOutputBin) Close() error {
	return nil
}
