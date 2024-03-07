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
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
)

// Bins are designed to hold a single stream, with any number of sources and sinks
type Bin struct {
	*Callbacks
	*StateManager

	pipeline *gst.Pipeline
	mu       sync.Mutex
	bin      *gst.Bin
	latency  uint64

	linkFunc   func() error
	eosFunc    func() bool
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
		Callbacks:    b.Callbacks,
		StateManager: b.StateManager,
		pipeline:     b.pipeline,
		bin:          gst.NewBin(name),
		pads:         make(map[string]*gst.GhostPad),
	}
}

// Add src as a source of b. This should only be called once for each source bin
func (b *Bin) AddSourceBin(src *Bin) error {
	logger.Debugw(fmt.Sprintf("adding src %s to %s", src.bin.GetName(), b.bin.GetName()))
	return b.addBin(src, gst.PadDirectionSource)
}

// Add src as a sink of b. This should only be called once for each sink bin
func (b *Bin) AddSinkBin(sink *Bin) error {
	logger.Debugw(fmt.Sprintf("adding sink %s to %s", sink.bin.GetName(), b.bin.GetName()))
	return b.addBin(sink, gst.PadDirectionSink)
}

func (b *Bin) addBin(bin *Bin, direction gst.PadDirection) error {
	bin.mu.Lock()
	alreadyAdded := bin.added
	bin.added = true
	bin.mu.Unlock()
	if alreadyAdded {
		return errors.ErrBinAlreadyAdded
	}

	b.LockStateShared()
	defer b.UnlockStateShared()

	state := b.GetStateLocked()
	if state > StateRunning {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if direction == gst.PadDirectionSource {
		b.srcs = append(b.srcs, bin)
	} else {
		b.sinks = append(b.sinks, bin)
	}

	if err := b.pipeline.Add(bin.bin.Element); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	if state == StateBuilding {
		return nil
	}

	if err := bin.link(); err != nil {
		return err
	}

	var err error
	bin.mu.Lock()
	if direction == gst.PadDirectionSource {
		err = linkPeersLocked(bin, b)
	} else {
		err = linkPeersLocked(b, bin)
	}
	bin.mu.Unlock()
	if err != nil {
		return err
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
	return b.removeBin(name, gst.PadDirectionSource)
}

func (b *Bin) RemoveSinkBin(name string) (bool, error) {
	return b.removeBin(name, gst.PadDirectionSink)
}

func (b *Bin) removeBin(name string, direction gst.PadDirection) (bool, error) {
	b.LockStateShared()
	defer b.UnlockStateShared()

	b.mu.Lock()
	defer b.mu.Unlock()

	var bin *Bin
	if direction == gst.PadDirectionSource {
		for i, s := range b.srcs {
			if s.bin.GetName() == name {
				bin = s
				b.srcs = append(b.srcs[:i], b.srcs[i+1:]...)
				break
			}
		}
	} else {
		for i, s := range b.sinks {
			if s.bin.GetName() == name {
				bin = s
				b.sinks = append(b.sinks[:i], b.sinks[i+1:]...)
				break
			}
		}
	}
	if bin == nil {
		return false, nil
	}

	state := b.GetStateLocked()
	if state > StateRunning {
		return true, nil
	}

	if state == StateBuilding {
		if err := b.pipeline.Remove(bin.bin.Element); err != nil {
			return false, errors.ErrGstPipelineError(err)
		}
		return true, nil
	}

	if direction == gst.PadDirectionSource {
		b.probeRemoveSource(bin)
	} else {
		b.probeRemoveSink(bin)
	}

	return true, nil
}

func (b *Bin) probeRemoveSource(src *Bin) {
	src.mu.Lock()
	srcGhostPad, sinkGhostPad, ok := deleteGhostPadsLocked(src, b)
	src.mu.Unlock()
	if !ok {
		return
	}

	sinkPad := sinkGhostPad.GetTarget()
	srcGhostPad.AddProbe(gst.PadProbeTypeBlocking, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		b.elements[0].ReleaseRequestPad(sinkPad)

		srcGhostPad.Unlink(sinkGhostPad.Pad)
		b.bin.RemovePad(sinkGhostPad.Pad)

		if _, err := glib.IdleAdd(func() bool {
			if err := b.pipeline.Remove(src.bin.Element); err != nil {
				logger.Warnw("failed to remove bin", err, "bin", src.bin.GetName())
				return false
			}
			if err := src.bin.SetState(gst.StateNull); err != nil {
				logger.Warnw("failed to change bin state", err, "bin", src.bin.GetName())
			}
			return false
		}); err != nil {
			logger.Errorw("failed to remove src bin", err, "bin", src.bin.GetName())
		}

		return gst.PadProbeRemove
	})
}

func (b *Bin) probeRemoveSink(sink *Bin) {
	sink.mu.Lock()
	srcGhostPad, sinkGhostPad, ok := deleteGhostPadsLocked(b, sink)
	sink.mu.Unlock()
	if !ok {
		return
	}

	srcGhostPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		srcGhostPad.Unlink(sinkGhostPad.Pad)
		sinkGhostPad.Pad.SendEvent(gst.NewEOSEvent())

		b.mu.Lock()
		err := b.pipeline.Remove(sink.bin.Element)
		b.mu.Unlock()

		if err != nil {
			b.OnError(errors.ErrGstPipelineError(err))
			return gst.PadProbeRemove
		}

		if err = sink.SetState(gst.StateNull); err != nil {
			logger.Warnw(fmt.Sprintf("failed to change %s state", sink.bin.GetName()), err)
		}

		b.elements[len(b.elements)-1].ReleaseRequestPad(srcGhostPad.GetTarget())
		b.bin.RemovePad(srcGhostPad.Pad)
		return gst.PadProbeRemove
	})
}

func deleteGhostPadsLocked(src, sink *Bin) (*gst.GhostPad, *gst.GhostPad, bool) {
	srcPad, srcOK := src.pads[sink.bin.GetName()]
	if !srcOK {
		logger.Errorw("source pad missing", nil, "bin", src.bin.GetName())
	}
	delete(src.pads, sink.bin.GetName())

	sinkPad, sinkOK := sink.pads[src.bin.GetName()]
	if !sinkOK {
		logger.Errorw("sink pad missing", nil, "bin", sink.bin.GetName())
	}
	delete(sink.pads, src.bin.GetName())

	return srcPad, sinkPad, srcOK && sinkOK
}

func (b *Bin) SetState(state gst.State) error {
	stateErr := make(chan error, 1)
	go func() {
		stateErr <- b.bin.SetState(state)
	}()
	select {
	case <-time.After(stateChangeTimeout):
		return errors.ErrPipelineFrozen
	case err := <-stateErr:
		if err != nil {
			return errors.ErrGstPipelineError(err)
		}
	}
	return nil
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

// Set a custom EOS function (used for appsrc, input-selector). If it returns true, EOS will also be sent to src bins
func (b *Bin) SetEOSFunc(f func() bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.eosFunc = f
}

func (b *Bin) sendEOS() {
	b.mu.Lock()
	eosFunc := b.eosFunc
	srcs := b.srcs
	b.mu.Unlock()

	if eosFunc != nil && !eosFunc() {
		return
	}

	if len(srcs) > 0 {
		var wg sync.WaitGroup
		wg.Add(len(b.srcs))
		for _, src := range srcs {
			go func(s *Bin) {
				s.sendEOS()
				wg.Done()
			}(src)
		}
		wg.Wait()
	} else if len(b.elements) > 0 {
		b.bin.SendEvent(gst.NewEOSEvent())
	}
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
					err = b.queueLinkPeersLocked(src, sink)
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
	srcPad, sinkPad, err := createGhostPadsLocked(src, sink, nil)
	if err != nil {
		return err
	}

	srcState := src.bin.GetCurrentState()
	sinkState := sink.bin.GetCurrentState()

	if srcState != sinkState {
		if srcState == gst.StateNull {
			srcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
				if padReturn := srcPad.Link(sinkPad.Pad); padReturn != gst.PadLinkOK {
					logger.Errorw("failed to link", errors.ErrPadLinkFailed(src.bin.GetName(), sink.bin.GetName(), padReturn.String()))
				}
				return gst.PadProbeRemove
			})
			return src.SetState(gst.StatePlaying)
		}

		if sinkState == gst.StateNull {
			srcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
				if err = sink.SetState(gst.StatePlaying); err != nil {
					src.OnError(errors.ErrGstPipelineError(err))
					return gst.PadProbeUnhandled
				}

				return gst.PadProbeRemove
			})
		}
	}

	if padReturn := srcPad.Link(sinkPad.Pad); padReturn != gst.PadLinkOK {
		return errors.ErrPadLinkFailed(src.bin.GetName(), sink.bin.GetName(), padReturn.String())
	}

	return nil
}

func (b *Bin) queueLinkPeersLocked(src, sink *Bin) error {
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

	srcPad, sinkPad, err := createGhostPadsLocked(src, sink, queue)
	if err != nil {
		return err
	}
	if padReturn := srcPad.Link(sinkPad.Pad); padReturn != gst.PadLinkOK {
		return errors.ErrPadLinkFailed(srcName, queueName, padReturn.String())
	}

	return nil
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
