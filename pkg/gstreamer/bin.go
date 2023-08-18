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
	BinTypeAudio BinType = "audio" // bin handles one audio data stream
	BinTypeVideo BinType = "video" // bin handles one video data stream
	BinTypeMuxed BinType = "muxed" // bin handles one stream of mixed/muxed audio and/or video
	BinTypeQueue BinType = "queue" // no elements, links all src bins to all sink bins using queues
)

// Bins are designed to hold a single stream, with any number of sources and sinks
type Bin struct {
	*Callbacks

	mu      sync.Mutex
	bin     *gst.Bin
	binType BinType
	latency uint64

	stateLock    sync.Mutex
	pendingState gst.State
	state        gst.State

	eosFunc func()

	srcs     []*Bin                   // source bins
	elements []*gst.Element           // elements within this bin
	queues   map[string]*gst.Element  // used to link srcs to sinks when there are no elements
	pads     map[string]*gst.GhostPad // ghost pads by bin name
	sinks    []*Bin                   // sink bins
}

func NewBin(name string, binType BinType, callbacks *Callbacks) *Bin {
	return &Bin{
		Callbacks: callbacks,
		bin:       gst.NewBin(name),
		binType:   binType,
		pads:      make(map[string]*gst.GhostPad),
	}
}

func NewQueueBin(name string, callbacks *Callbacks) *Bin {
	return &Bin{
		Callbacks: callbacks,
		bin:       gst.NewBin(name),
		binType:   BinTypeQueue,
		queues:    make(map[string]*gst.Element),
	}
}

func (b *Bin) AddSourceBin(src *Bin) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.srcs = append(b.srcs, src)

	if b.state == gst.StatePlaying {
		if err := src.link(); err != nil {
			return err
		}

		src.mu.Lock()
		srcPad, sinkPad, err := createGhostPads(src, b)
		src.mu.Unlock()
		if err != nil {
			return err
		}
		if padReturn := srcPad.Link(sinkPad.Pad); padReturn != gst.PadLinkOK {
			return errors.ErrPadLinkFailed(src.bin.GetName(), b.bin.GetName(), padReturn.String())
		}
	}

	if err := b.bin.Add(src.bin.Element); err != nil {
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

	if b.state != gst.StatePlaying {
		if err := b.bin.Remove(src.bin.Element); err != nil {
			return false, errors.ErrGstPipelineError(err)
		}
		return true, nil
	}

	if err := src.downgradeStateTo(gst.StateNull); err != nil {
		return false, err
	}

	src.mu.Lock()
	srcPad, sinkPad, err := removeGhostPads(src, b)
	src.mu.Unlock()
	if err != nil {
		return false, err
	}
	srcPad.Unlink(sinkPad.Pad)

	if err = b.bin.Remove(src.bin.Element); err != nil {
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
	if err := b.bin.Add(sink.bin.Element); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if b.state == gst.StatePlaying {
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

	if b.state != gst.StatePlaying {
		if err := b.bin.Remove(sink.bin.Element); err != nil {
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
		err = b.bin.Remove(sink.bin.Element)
		b.mu.Unlock()
		if err != nil {
			b.OnError(errors.ErrGstPipelineError(err))
			return gst.PadProbeRemove
		}

		if err = sink.downgradeStateTo(gst.StateNull); err != nil {
			logger.Warnw(fmt.Sprintf("failed to change %s state", sink.bin.GetName()), err)
		}

		b.elements[len(b.elements)-1].ReleaseRequestPad(srcPad.GetTarget())
		return gst.PadProbeRemove
	})

	return true, nil
}

func (b *Bin) SetState(state gst.State) error {
	b.stateLock.Lock()
	b.pendingState = state
	b.stateLock.Unlock()

	for {
		b.stateLock.Lock()

		switch {
		case b.pendingState != state || b.state == b.pendingState:
			b.stateLock.Unlock()
			return nil

		case b.state < b.pendingState:
			nextState := b.state + 1
			if err := b.upgradeStateTo(nextState); err != nil {
				return err
			}
			b.state = nextState

		case b.state > b.pendingState:
			nextState := b.state - 1
			if err := b.downgradeStateTo(nextState); err != nil {
				return err
			}
			b.state = nextState
		}

		b.stateLock.Unlock()
	}
}

// ----- Internal -----

func (b *Bin) link() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	logger.Debugw("linking", "bin", b.bin.GetName())

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

	if b.binType == BinTypeQueue {
		// link src bins to sink bins using queues
		b.queues = make(map[string]*gst.Element)
		for _, src := range b.srcs {
			src.mu.Lock()
			for _, sink := range b.sinks {
				sink.mu.Lock()
				err := b.queueLink(src, sink)
				sink.mu.Unlock()
				if err != nil {
					src.mu.Unlock()
					return err
				}
			}
			src.mu.Unlock()
		}
	} else {
		// link elements
		if err := gst.ElementLinkMany(b.elements...); err != nil {
			return errors.ErrGstPipelineError(err)
		}

		// link src bins to elements
		for _, src := range b.srcs {
			src.mu.Lock()
			srcPad, sinkPad, err := createGhostPads(src, b)
			src.mu.Unlock()
			if err != nil {
				return err
			}
			if padReturn := srcPad.Link(sinkPad.Pad); padReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed(src.bin.GetName(), b.bin.GetName(), padReturn.String())
			}
		}

		// link elements to sink bins
		for _, sink := range b.sinks {
			sink.mu.Lock()
			srcPad, sinkPad, err := createGhostPads(b, sink)
			sink.mu.Unlock()
			if err != nil {
				return err
			}
			if padReturn := srcPad.Link(sinkPad.Pad); padReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed(b.bin.GetName(), sink.bin.GetName(), padReturn.String())
			}
		}
	}

	return nil
}

func (b *Bin) queueLink(src, sink *Bin) error {
	srcName := src.bin.GetName()
	sinkName := sink.bin.GetName()
	queueName := fmt.Sprintf("%s_queue_%s", srcName, sinkName)
	queue, err := BuildQueue(queueName, b.latency, true)
	if err != nil {
		return err
	}

	b.queues[queueName] = queue
	if err = b.bin.Add(queue); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	srcPad, sinkPad, err := createGhostPads(src, sink)
	if err != nil {
		return err
	}
	if padReturn := srcPad.Link(queue.GetStaticPad("sink")); padReturn != gst.PadLinkOK {
		return errors.ErrPadLinkFailed(srcName, queueName, padReturn.String())
	}
	if padReturn := queue.GetStaticPad("src").Link(sinkPad.Pad); padReturn != gst.PadLinkOK {
		return errors.ErrPadLinkFailed(queueName, sinkName, padReturn.String())
	}

	return nil
}

func createGhostPads(src, sink *Bin) (*gst.GhostPad, *gst.GhostPad, error) {
	srcElement := src.elements[len(src.elements)-1]
	sinkElement := sink.elements[0]

	var srcPad, sinkPad *gst.GhostPad
	if src.binType != BinTypeMuxed || sink.binType == BinTypeMuxed {
		srcPad = src.createGhostPad(srcElement, "src", sink)
	} else {
		srcPad = src.createGhostPad(srcElement, string(sink.binType), sink)
	}
	if sink.binType != BinTypeMuxed || src.binType == BinTypeMuxed {
		sinkPad = sink.createGhostPad(sinkElement, "sink", src)
	} else {
		sinkPad = sink.createGhostPad(sinkElement, string(src.binType), src)
	}

	if srcPad == nil || sinkPad == nil {
		return nil, nil, errors.ErrGhostPadFailed
	}

	src.pads[sink.bin.GetName()] = srcPad
	sink.pads[src.bin.GetName()] = sinkPad
	return srcPad, sinkPad, nil
}

func (b *Bin) createGhostPad(e *gst.Element, padFormat string, other *Bin) *gst.GhostPad {
	if pad := e.GetStaticPad(padFormat); pad != nil {
		return gst.NewGhostPad(fmt.Sprintf("%s_%s", b.bin.GetName(), padFormat), pad)
	}
	if pad := e.GetRequestPad(fmt.Sprintf("%s_%%u", padFormat)); pad != nil {
		return gst.NewGhostPad(fmt.Sprintf("%s_%s_%s", b.bin.GetName(), padFormat, other.bin.GetName()), pad)
	}
	return nil
}

func removeGhostPads(src, sink *Bin) (*gst.GhostPad, *gst.GhostPad, error) {
	var srcPad, sinkPad *gst.GhostPad

	srcPad = src.pads[sink.bin.GetName()]
	delete(src.pads, sink.bin.GetName())

	sinkPad = sink.pads[src.bin.GetName()]
	delete(sink.pads, src.bin.GetName())

	if srcPad == nil || sinkPad == nil {
		return nil, nil, errors.ErrGhostPadFailed
	}

	return srcPad, sinkPad, nil
}

func (b *Bin) upgradeStateTo(state gst.State) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, sink := range b.sinks {
		if err := sink.upgradeStateTo(state); err != nil {
			return err
		}
	}
	if err := b.bin.BlockSetState(state); err != nil {
		return errors.ErrStateChangeFailed(b.bin.GetName(), state)
	}

	b.state = state
	return nil
}

func (b *Bin) downgradeStateTo(state gst.State) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, src := range b.srcs {
		if err := src.downgradeStateTo(state); err != nil {
			return err
		}
	}
	if b.state > state {
		if err := b.bin.BlockSetState(state); err != nil {
			return errors.ErrStateChangeFailed(b.bin.GetName(), state)
		}
	}

	b.state = state
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
