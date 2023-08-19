// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gstreamer

import (
	"fmt"
	"sync"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
)

type BinType string

const (
	BinTypeAudio       BinType = "audio" // bin handles one audio data stream
	BinTypeVideo       BinType = "video" // bin handles one video data stream
	BinTypeMuxed       BinType = "muxed" // bin handles one stream of mixed/muxed audio and/or video
	BinTypeMultiStream BinType = "multi" // bin has no elements of its own, but handles multiple streams
)

// Bins are designed to hold a single stream, with any number of sources and sinks
type Bin struct {
	*Callbacks

	pipeline *gst.Pipeline
	mu       sync.Mutex
	bin      *gst.Bin
	binType  BinType
	latency  uint64

	linkFunc func() error
	eosFunc  func()

	srcs     []*Bin                   // source bins
	elements []*gst.Element           // elements within this bin
	queues   map[string]*gst.Element  // used with BinTypeMultiStream
	pads     map[string]*gst.GhostPad // ghost pads by bin name
	sinks    []*Bin                   // sink bins
}

func (p *Pipeline) NewBin(name string, binType BinType) *Bin {
	logger.Debugw(fmt.Sprintf("creating bin %s", name))
	return &Bin{
		Callbacks: p.Callbacks,
		pipeline:  p.pipeline,
		bin:       gst.NewBin(name),
		binType:   binType,
		pads:      make(map[string]*gst.GhostPad),
	}
}

func (b *Bin) AddSourceBin(src *Bin) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.srcs = append(b.srcs, src)

	if b.bin.GetState() == gst.StatePlaying {
		if err := src.link(); err != nil {
			return err
		}

		src.mu.Lock()
		err := linkPeers(src, b)
		src.mu.Unlock()
		if err != nil {
			return err
		}
	}

	if err := b.pipeline.Add(src.bin.Element); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	src.bin.SyncStateWithParent()
	return nil
}

func (b *Bin) RemoveSourceBin(name string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	var src *Bin
	for i, s := range b.srcs {
		if s.bin.GetName() == name {
			src = s
			b.srcs = append(b.srcs[:i], b.srcs[i+1:]...)
			break
		}
		removed, err := s.RemoveSourceBin(name)
		if removed || err != nil {
			return removed, err
		}
	}
	if src == nil {
		return false, nil
	}

	if b.bin.GetState() != gst.StatePlaying {
		if err := b.pipeline.Remove(src.bin.Element); err != nil {
			return false, errors.ErrGstPipelineError(err)
		}
		return true, nil
	}

	if err := src.bin.SetState(gst.StateNull); err != nil {
		return false, err
	}

	src.mu.Lock()
	srcPad, sinkPad, err := removeGhostPads(src, b)
	src.mu.Unlock()
	if err != nil {
		return false, err
	}
	srcPad.Unlink(sinkPad.Pad)

	if err = b.pipeline.Remove(src.bin.Element); err != nil {
		return false, errors.ErrGstPipelineError(err)
	}

	b.elements[0].ReleaseRequestPad(sinkPad.GetTarget())
	return true, nil
}

func (b *Bin) AddElement(e *gst.Element) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.elements = append(b.elements, e)
	if err := b.bin.Add(e); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	return nil
}

func (b *Bin) AddElements(elements ...*gst.Element) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.elements = append(b.elements, elements...)
	if err := b.bin.AddMany(elements...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	return nil
}

func (b *Bin) AddSinkBin(sink *Bin) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.sinks = append(b.sinks, sink)
	if err := b.pipeline.Add(sink.bin.Element); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if b.bin.GetState() == gst.StatePlaying {
		sink.mu.Lock()
		srcPad, sinkPad, err := createGhostPads(b, sink)
		sink.mu.Unlock()
		if err != nil {
			return err
		}

		srcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
			if padReturn := srcPad.Link(sinkPad.Pad); padReturn != gst.PadLinkOK {
				b.OnError(errors.ErrPadLinkFailed(b.bin.GetName(), sink.bin.GetName(), padReturn.String()))
				return gst.PadProbeUnhandled
			}

			sink.bin.SyncStateWithParent()
			return gst.PadProbeRemove
		})
	}

	return nil
}

func (b *Bin) RemoveSinkBin(name string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	var sink *Bin
	for i, s := range b.sinks {
		if s.bin.GetName() == name {
			sink = s
			b.sinks = append(b.sinks[:i], b.sinks[i+1:]...)
			break
		}
		removed, err := s.RemoveSinkBin(name)
		if removed || err != nil {
			return removed, err
		}
	}
	if sink == nil {
		return false, nil
	}

	if b.bin.GetState() != gst.StatePlaying {
		if err := b.pipeline.Remove(sink.bin.Element); err != nil {
			return false, errors.ErrGstPipelineError(err)
		}
		return true, nil
	}

	sink.mu.Lock()
	srcPad, sinkPad, err := removeGhostPads(b, sink)
	sink.mu.Unlock()
	if err != nil {
		return false, err
	}

	srcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		srcPad.Unlink(sinkPad.Pad)
		sinkPad.Pad.SendEvent(gst.NewEOSEvent())

		b.mu.Lock()
		err = b.pipeline.Remove(sink.bin.Element)
		b.mu.Unlock()
		if err != nil {
			b.OnError(errors.ErrGstPipelineError(err))
			return gst.PadProbeRemove
		}

		if err = sink.bin.SetState(gst.StateNull); err != nil {
			logger.Warnw(fmt.Sprintf("failed to change %s state", sink.bin.GetName()), err)
		}

		b.elements[len(b.elements)-1].ReleaseRequestPad(srcPad.GetTarget())
		return gst.PadProbeRemove
	})

	return true, nil
}

func (b *Bin) SetState(state gst.State) error {
	return b.bin.SetState(state)
}

func (b *Bin) SetLinkFunc(f func() error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.linkFunc = f
}

func (b *Bin) SetEOSFunc(f func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.eosFunc = f
}

// ----- Internal -----

func (b *Bin) link() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, src := range b.srcs {
		if err := src.link(); err != nil {
			return err
		}
	}
	for _, sink := range b.sinks {
		if err := sink.link(); err != nil {
			return err
		}
	}

	switch {
	case b.linkFunc != nil:
		return b.linkFunc()

	case b.binType == BinTypeMultiStream:
		// link all src bins to all sink bins
		addQueues := len(b.sinks) > 1
		for _, src := range b.srcs {
			src.mu.Lock()
			for _, sink := range b.sinks {
				sink.mu.Lock()
				var err error
				if addQueues {
					err = b.linkPeersWithQueue(src, sink)
				} else {
					err = linkPeers(src, sink)
				}
				sink.mu.Unlock()
				if err != nil {
					src.mu.Unlock()
					return err
				}
			}
			src.mu.Unlock()
		}
		return nil

	default:
		// link elements
		if err := gst.ElementLinkMany(b.elements...); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		// link src bins to elements
		for _, src := range b.srcs {
			src.mu.Lock()
			err := linkPeers(src, b)
			src.mu.Unlock()
			if err != nil {
				return err
			}
		}

		// link elements to sink bins
		for _, sink := range b.sinks {
			sink.mu.Lock()
			err := linkPeers(b, sink)
			sink.mu.Unlock()
			if err != nil {
				return err
			}
		}

		return nil
	}
}

func linkPeers(src, sink *Bin) error {
	srcPad, sinkPad, err := createGhostPads(src, sink)
	if err != nil {
		return err
	}
	if padReturn := srcPad.Link(sinkPad.Pad); padReturn != gst.PadLinkOK {
		return errors.ErrPadLinkFailed(src.bin.GetName(), sink.bin.GetName(), padReturn.String())
	}
	return nil
}

func (b *Bin) linkPeersWithQueue(src, sink *Bin) error {
	srcName := src.bin.GetName()
	sinkName := sink.bin.GetName()

	queueName := fmt.Sprintf("%s_%s_queue", srcName, sinkName)
	queue, err := BuildQueue(queueName, b.latency, true)
	if err != nil {
		return err
	}
	b.queues[queueName] = queue
	if err = sink.bin.Add(queue); err != nil {
		return err
	}

	srcPad, sinkPad, err := createQueueGhostPads(src, sink, queue)
	if err != nil {
		return err
	}
	if padReturn := srcPad.Link(sinkPad.Pad); padReturn != gst.PadLinkOK {
		return errors.ErrPadLinkFailed(srcName, queueName, padReturn.String())
	}

	sinkElement := sink.elements[0]
	var pad *gst.Pad
	if sink.binType != BinTypeMuxed || src.binType == BinTypeMuxed {
		pad = getPad(sinkElement, "sink")
	} else {
		pad = getPad(sinkElement, string(src.binType))
	}
	if padReturn := queue.GetStaticPad("src").Link(pad); padReturn != gst.PadLinkOK {
		return errors.ErrPadLinkFailed(queueName, sinkName, padReturn.String())
	}

	return nil
}

func (b *Bin) sendEOS() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.eosFunc != nil {
		b.eosFunc()
	} else if len(b.srcs) > 0 {
		for _, src := range b.srcs {
			src.sendEOS()
		}
	} else if len(b.elements) > 0 {
		b.bin.SendEvent(gst.NewEOSEvent())
	}
}
