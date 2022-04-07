package output

import (
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
)

func buildFileOutputBin(p *params.Params) (*Bin, error) {
	// create elements
	sink, err := gst.NewElement("filesink")
	if err != nil {
		return nil, err
	}
	if err = sink.SetProperty("location", p.FileInfo.Filename); err != nil {
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

	return &Bin{
		bin:      bin,
		fileSink: sink,
		logger:   p.Logger,
	}, nil
}
