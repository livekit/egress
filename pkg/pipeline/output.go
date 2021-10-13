// +build !test

package pipeline

import (
	"fmt"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
	"github.com/tinyzimmer/go-gst/gst"
)

type OutputBin struct {
	isStream bool
	bin      *gst.Bin

	// file only
	fileSink *gst.Element

	// rtmp only
	tee  *gst.Element
	rtmp map[string]*RtmpOut
}

type RtmpOut struct {
	pad   string
	queue *gst.Element
	sink  *gst.Element
}

func newFileOutputBin(filename string) (*OutputBin, error) {
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
		return nil, ErrGhostPadFailed
	}

	return &OutputBin{
		isStream: false,
		bin:      bin,
		fileSink: sink,
	}, nil
}

func newRtmpOutputBin(urls []string) (*OutputBin, error) {
	// create elements
	tee, err := gst.NewElement("tee")
	if err != nil {
		return nil, err
	}

	bin := gst.NewBin("output")
	if err = bin.Add(tee); err != nil {
		return nil, err
	}

	rtmpOut := make(map[string]*RtmpOut)
	for _, url := range urls {
		rtmp, err := createRtmpOut(url)
		if err != nil {
			return nil, err
		}

		if err = bin.AddMany(rtmp.queue, rtmp.sink); err != nil {
			return nil, err
		}

		rtmpOut[url] = rtmp
	}

	// add ghost pad
	ghostPad := gst.NewGhostPad("sink", tee.GetStaticPad("sink"))
	if !bin.AddPad(ghostPad.Pad) {
		return nil, ErrGhostPadFailed
	}

	return &OutputBin{
		isStream: true,
		bin:      bin,
		tee:      tee,
		rtmp:     rtmpOut,
	}, nil
}

func createRtmpOut(url string) (*RtmpOut, error) {
	id := utils.NewGuid("")

	queue, err := gst.NewElementWithName("queue", fmt.Sprintf("queue_%s", id))
	if err != nil {
		return nil, err
	}
	queue.SetArg("leaky", "downstream")

	sink, err := gst.NewElementWithName("rtmpsink", fmt.Sprintf("sink_%s", id))
	if err != nil {
		return nil, err
	}
	if err = sink.SetProperty("sync", false); err != nil {
		return nil, err
	}
	if err = sink.Set("location", url); err != nil {
		return nil, err
	}

	return &RtmpOut{
		queue: queue,
		sink:  sink,
	}, nil
}

func (b *OutputBin) Link() error {
	if !b.isStream {
		return nil
	}

	for _, rtmp := range b.rtmp {
		// link queue to rtmp sink
		if err := rtmp.queue.Link(rtmp.sink); err != nil {
			return err
		}

		pad := b.tee.GetRequestPad("src_%u")
		rtmp.pad = pad.GetName()

		// link tee to queue
		if err := requireLink(pad, rtmp.queue.GetStaticPad("sink")); err != nil {
			return err
		}
	}

	return nil
}

func (b *OutputBin) AddRtmpSink(url string) error {
	if !b.isStream {
		return ErrCannotAddToFile
	}

	if _, ok := b.rtmp[url]; ok {
		return ErrOutputAlreadyExists
	}

	rtmp, err := createRtmpOut(url)
	if err != nil {
		return err
	}

	// add to bin
	if err = b.bin.AddMany(rtmp.queue, rtmp.sink); err != nil {
		return err
	}

	// link queue to sink
	if err = rtmp.queue.Link(rtmp.sink); err != nil {
		_ = b.bin.RemoveMany(rtmp.queue, rtmp.sink)
		return err
	}

	teeSrcPad := b.tee.GetRequestPad("src_%u")
	rtmp.pad = teeSrcPad.GetName()

	teeSrcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		// link tee to queue
		if err = requireLink(pad, rtmp.queue.GetStaticPad("sink")); err != nil {
			logger.Errorw("failed to link tee to queue", err)
		}

		// sync state
		rtmp.queue.SyncStateWithParent()
		rtmp.sink.SyncStateWithParent()

		return gst.PadProbeRemove
	})

	b.rtmp[url] = rtmp
	return nil
}

func (b *OutputBin) RemoveRtmpSink(url string) error {
	if !b.isStream {
		return ErrCannotRemoveFromFile
	}

	rtmp, ok := b.rtmp[url]
	if !ok {
		return ErrOutputNotFound
	}

	srcPad := b.tee.GetStaticPad(rtmp.pad)
	srcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		// remove probe
		pad.RemoveProbe(uint64(info.ID()))

		// unlink queue
		pad.Unlink(rtmp.queue.GetStaticPad("sink"))

		// send EOS to queue
		rtmp.queue.GetStaticPad("sink").SendEvent(gst.NewEOSEvent())

		// remove from bin
		if err := b.bin.RemoveMany(rtmp.queue, rtmp.sink); err != nil {
			logger.Errorw("failed to remove rtmp queue", err)
		}
		if err := rtmp.queue.SetState(gst.StateNull); err != nil {
			logger.Errorw("failed stop rtmp queue", err)
		}
		if err := rtmp.sink.SetState(gst.StateNull); err != nil {
			logger.Errorw("failed to stop rtmp sink", err)
		}

		// release tee src pad
		b.tee.ReleaseRequestPad(pad)

		return gst.PadProbeOK
	})

	delete(b.rtmp, url)
	return nil
}

func (b *OutputBin) RemoveSinkByName(name string) error {
	if !b.isStream {
		return ErrCannotRemoveFromFile
	}

	for url, rtmp := range b.rtmp {
		if rtmp.queue.GetName() == name || rtmp.sink.GetName() == name {
			return b.RemoveRtmpSink(url)
		}
	}

	return ErrOutputNotFound
}
