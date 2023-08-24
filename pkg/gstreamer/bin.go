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

// Bins are designed to hold a single stream, with any number of sources and sinks
type Bin struct {
	*Callbacks

	pipeline *gst.Pipeline
	mu       sync.Mutex
	bin      *gst.Bin
	latency  uint64

	linkFunc   func() error
	eosFunc    func()
	getSrcPad  func(string) *gst.Pad
	getSinkPad func(string) *gst.Pad

	added    bool
	srcs     []*Bin                   // source bins
	elements []*gst.Element           // elements within this bin
	queues   map[string]*gst.Element  // used with BinTypeMultiStream
	pads     map[string]*gst.GhostPad // ghost pads by bin name
	sinks    []*Bin                   // sink bins
}

func (b *Bin) NewBin(name string) *Bin {
	return &Bin{
		Callbacks: b.Callbacks,
		pipeline:  b.pipeline,
		bin:       gst.NewBin(name),
		pads:      make(map[string]*gst.GhostPad),
	}
}

// Add src as a source of b. This should only be called once for each source bin
func (b *Bin) AddSourceBin(src *Bin) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	src.mu.Lock()
	alreadyAdded := src.added
	src.added = true
	src.mu.Unlock()
	if alreadyAdded {
		return errors.ErrBinAlreadyAdded
	}

	b.srcs = append(b.srcs, src)
	if err := b.pipeline.Add(src.bin.Element); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if b.bin.GetState() == gst.StatePlaying {
		if err := src.link(); err != nil {
			return err
		}

		src.mu.Lock()
		err := linkPeersLocked(src, b)
		src.mu.Unlock()
		if err != nil {
			return err
		}

		if err = src.bin.SetState(gst.StatePlaying); err != nil {
			return err
		}
	}

	return nil
}

// Add src as a sink of b. This should only be called once for each sink bin
func (b *Bin) AddSinkBin(sink *Bin) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	sink.mu.Lock()
	alreadyAdded := sink.added
	sink.added = true
	sink.mu.Unlock()
	if alreadyAdded {
		return errors.ErrBinAlreadyAdded
	}

	b.sinks = append(b.sinks, sink)
	if err := b.pipeline.Add(sink.bin.Element); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if b.bin.GetState() == gst.StatePlaying {
		if err := sink.link(); err != nil {
			return err
		}

		sink.mu.Lock()
		err := linkPeersLocked(b, sink)
		sink.mu.Unlock()
		if err != nil {
			return err
		}
	}

	return nil
}

// Elements will be linked in the order they are added
func (b *Bin) AddElement(e *gst.Element) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.elements = append(b.elements, e)
	if err := b.bin.Add(e); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	return nil
}

// Elements will be linked in the order they are added
func (b *Bin) AddElements(elements ...*gst.Element) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.elements = append(b.elements, elements...)
	if err := b.bin.AddMany(elements...); err != nil {
		return errors.ErrGstPipelineError(err)
	}
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
	srcPad, sinkPad := getGhostPads(src, b)
	src.mu.Unlock()

	srcPad.Unlink(sinkPad.Pad)
	if err := b.pipeline.Remove(src.bin.Element); err != nil {
		return false, errors.ErrGstPipelineError(err)
	}

	b.bin.RemovePad(sinkPad.Pad)
	b.elements[0].ReleaseRequestPad(sinkPad.GetTarget())
	return true, nil
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
	srcPad, sinkPad := getGhostPads(b, sink)
	sink.mu.Unlock()

	srcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		srcPad.Unlink(sinkPad.Pad)
		sinkPad.Pad.SendEvent(gst.NewEOSEvent())

		b.mu.Lock()
		err := b.pipeline.Remove(sink.bin.Element)
		b.mu.Unlock()
		if err != nil {
			b.OnError(errors.ErrGstPipelineError(err))
			return gst.PadProbeRemove
		}

		if err = sink.bin.SetState(gst.StateNull); err != nil {
			logger.Warnw(fmt.Sprintf("failed to change %s state", sink.bin.GetName()), err)
		}

		b.elements[len(b.elements)-1].ReleaseRequestPad(srcPad.GetTarget())
		b.bin.RemovePad(srcPad.Pad)
		return gst.PadProbeRemove
	})

	return true, nil
}

func (b *Bin) SetState(state gst.State) error {
	return b.bin.SetState(state)
}

// Set a custom linking function for this bin's elements (used when you need to modify chain functions)
func (b *Bin) SetLinkFunc(f func() error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.linkFunc = f
}

// Set a custom linking function which returns a pad for the named src bin
func (b *Bin) SetGetSrcPad(f func(srcName string) *gst.Pad) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.getSrcPad = f
}

// Set a custom linking function which returns a pad for the named sink bin
func (b *Bin) SetGetSinkPad(f func(sinkName string) *gst.Pad) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.getSinkPad = f
}

// Set a custom EOS function (used for appsrc)
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

	if len(b.elements) > 0 {
		if b.linkFunc != nil {
			if err := b.linkFunc(); err != nil {
				return err
			}
		} else {
			// link elements
			if err := gst.ElementLinkMany(b.elements...); err != nil {
				return errors.ErrGstPipelineError(err)
			}
		}

		for _, src := range getPeerSrcs(b.srcs) {
			src.mu.Lock()
			err := linkPeersLocked(src, b)
			src.mu.Unlock()
			if err != nil {
				return err
			}
		}

		for _, sink := range getPeerSinks(b.sinks) {
			sink.mu.Lock()
			err := linkPeersLocked(b, sink)
			sink.mu.Unlock()
			if err != nil {
				return err
			}
		}
	} else {
		// link src bins to sink bins
		srcs := getPeerSrcs(b.srcs)
		sinks := getPeerSinks(b.sinks)

		addQueues := len(sinks) > 1
		for _, src := range srcs {
			src.mu.Lock()
			for _, sink := range sinks {
				sink.mu.Lock()
				var err error
				if addQueues {
					err = b.linkPeersWithQueueLocked(src, sink)
				} else {
					err = linkPeersLocked(src, sink)
				}
				sink.mu.Unlock()
				if err != nil {
					src.mu.Unlock()
					return err
				}
			}
			src.mu.Unlock()
		}
	}

	return nil
}

func linkPeersLocked(src, sink *Bin) error {
	srcPad, sinkPad, err := createGhostPads(src, sink)
	if err != nil {
		return err
	}

	if src.bin.GetState() == gst.StatePlaying {
		srcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
			if err = sink.bin.SetState(gst.StatePlaying); err != nil {
				src.OnError(errors.ErrGstPipelineError(err))
				return gst.PadProbeUnhandled
			}

			return gst.PadProbeRemove
		})
	}

	if padReturn := srcPad.Link(sinkPad.Pad); padReturn != gst.PadLinkOK {
		return errors.ErrPadLinkFailed(src.bin.GetName(), sink.bin.GetName(), padReturn.String())
	}

	return nil
}

func (b *Bin) linkPeersWithQueueLocked(src, sink *Bin) error {
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

	srcPad, sinkPad, err := createGhostPadsWithQueue(src, sink, queue)
	if err != nil {
		return err
	}
	if padReturn := srcPad.Link(sinkPad.Pad); padReturn != gst.PadLinkOK {
		return errors.ErrPadLinkFailed(srcName, queueName, padReturn.String())
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

func getPeerSrcs(srcs []*Bin) []*Bin {
	flattened := make([]*Bin, 0, len(srcs))
	for _, src := range srcs {
		if len(src.elements) > 0 {
			flattened = append(flattened, src)
		} else {
			flattened = append(flattened, getPeerSrcs(src.srcs)...)
		}
	}
	return flattened
}

func getPeerSinks(sinks []*Bin) []*Bin {
	flattened := make([]*Bin, 0, len(sinks))
	for _, sink := range sinks {
		if len(sink.elements) > 0 {
			flattened = append(flattened, sink)
		} else {
			flattened = append(flattened, getPeerSinks(sink.sinks)...)
		}
	}
	return flattened
}
