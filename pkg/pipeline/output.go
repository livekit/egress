// +build !test

package pipeline

import (
	"fmt"

	"github.com/livekit/protocol/logger"
	"github.com/tinyzimmer/go-gst/gst"
)

type OutputBin struct {
	isStream bool
	bin      *gst.Bin

	// file only
	fileSink *gst.Element

	// rtmp only
	tee       *gst.Element
	urls      map[string]int
	queues    []*gst.Element
	rtmpSinks []*gst.Element
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

	indexes := make(map[string]int)
	queues := make([]*gst.Element, len(urls))
	sinks := make([]*gst.Element, len(urls))
	for i, url := range urls {
		indexes[url] = i

		queues[i], err = gst.NewElement("queue")
		if err != nil {
			return nil, err
		}

		sink, err := gst.NewElement("rtmpsink")
		if err != nil {
			return nil, err
		}
		if err = sink.SetProperty("sync", false); err != nil {
			return nil, err
		}
		if err = sink.Set("location", url); err != nil {
			return nil, err
		}
		sinks[i] = sink
	}

	// create bin
	if err = bin.AddMany(queues...); err != nil {
		return nil, err
	}
	if err = bin.AddMany(sinks...); err != nil {
		return nil, err
	}

	// add ghost pad
	ghostPad := gst.NewGhostPad("sink", tee.GetStaticPad("sink"))
	if !bin.AddPad(ghostPad.Pad) {
		return nil, ErrGhostPadFailed
	}

	return &OutputBin{
		isStream:  true,
		bin:       bin,
		tee:       tee,
		urls:      indexes,
		queues:    queues,
		rtmpSinks: sinks,
	}, nil
}

func (b *OutputBin) Link() error {
	if !b.isStream {
		return nil
	}

	for i, q := range b.queues {
		// link queue to rtmp sink
		if err := q.Link(b.rtmpSinks[i]); err != nil {
			return err
		}

		// link tee to queue
		if err := requireLink(
			b.tee.GetRequestPad(fmt.Sprintf("src_%d", i)),
			q.GetStaticPad("sink")); err != nil {
			return err
		}
	}

	return nil
}

func (b *OutputBin) AddRtmpSink(url string) error {
	if !b.isStream {
		return ErrCannotAddToFile
	}

	if _, ok := b.urls[url]; ok {
		return ErrOutputAlreadyExists
	}

	idx := -1
	for i, q := range b.queues {
		if q == nil {
			idx = i
			break
		}
	}

	queue, err := gst.NewElement("queue")
	if err != nil {
		return err
	}
	sink, err := gst.NewElement("rtmpsink")
	if err != nil {
		return err
	}
	if err = sink.SetProperty("sync", false); err != nil {
		return err
	}
	if err = sink.Set("location", url); err != nil {
		return err
	}

	// add to bin
	if err = b.bin.AddMany(queue, sink); err != nil {
		return err
	}

	if idx == -1 {
		idx = len(b.urls)
		b.queues = append(b.queues, queue)
		b.rtmpSinks = append(b.rtmpSinks, sink)
	} else {
		b.queues[idx] = queue
		b.rtmpSinks[idx] = sink
	}
	b.urls[url] = idx

	// link queue to sink
	if err = queue.Link(sink); err != nil {
		return err
	}

	teeSrcPad := b.tee.GetRequestPad(fmt.Sprintf("src_%d", idx))
	teeSrcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		// link tee to queue
		if err = requireLink(pad, queue.GetStaticPad("sink")); err != nil {
			logger.Errorw("failed to link tee to queue", err)
		}

		// sync state
		queue.SyncStateWithParent()
		sink.SyncStateWithParent()

		return gst.PadProbeRemove
	})

	return nil
}

func (b *OutputBin) RemoveRtmpSink(url string) error {
	if !b.isStream {
		return ErrCannotRemoveFromFile
	}

	idx, ok := b.urls[url]
	if !ok {
		return ErrOutputNotFound
	}

	queue := b.queues[idx]
	sink := b.rtmpSinks[idx]
	srcPad := b.tee.GetStaticPad(fmt.Sprintf("src_%d", idx))
	srcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		// remove probe
		pad.RemoveProbe(uint64(info.ID()))

		// unlink queue
		pad.Unlink(queue.GetStaticPad("sink"))

		// send EOS to queue
		queue.GetStaticPad("sink").SendEvent(gst.NewEOSEvent())

		// remove from bin
		if err := b.bin.RemoveMany(queue, sink); err != nil {
			logger.Errorw("failed to remove rtmp queue", err)
		}
		if err := queue.SetState(gst.StateNull); err != nil {
			logger.Errorw("failed stop rtmp queue", err)
		}
		if err := sink.SetState(gst.StateNull); err != nil {
			logger.Errorw("failed to stop rtmp sink", err)
		}

		// release tee src pad
		b.tee.ReleaseRequestPad(pad)

		return gst.PadProbeOK
	})

	delete(b.urls, url)
	b.queues[idx] = nil
	b.rtmpSinks[idx] = nil
	return nil
}
