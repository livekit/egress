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

type DataType string

const (
	DataTypeAudio DataType = "audio"
	DataTypeVideo DataType = "video"
	DataTypeMuxed DataType = "muxed"
)

type Bin struct {
	mu       sync.Mutex
	dataType DataType
	state    gst.State

	srcs     []*Bin
	bin      *gst.Bin
	elements []*gst.Element
	sinks    []*Bin
	pads     map[string]*gst.GhostPad

	eosFunc    func()      // custom EOS function
	errHandler func(error) // async error handler
	finalizer  func()      // custom finalizer
}

func NewBin(name string, dataType DataType) *Bin {
	return &Bin{
		bin:      gst.NewBin(name),
		dataType: dataType,
		state:    gst.StateNull,
	}
}

func (b *Bin) AddSourceBin(src *Bin) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	src.setErrorHandler(b.errHandler)
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

	if b.state != src.state {
		src.bin.SyncStateWithParent()
	}

	return nil
}

func (b *Bin) RemoveSourceBin(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	var src *Bin
	for i, s := range b.srcs {
		if s.bin.GetName() == name {
			src = s
			b.srcs = append(b.srcs[:i], b.srcs[i+1:]...)
			break
		}
	}
	if src == nil {
		return nil
	}

	if b.state != gst.StatePlaying {
		if err := b.bin.Remove(src.bin.Element); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		return nil
	}

	if err := src.downgradeState(gst.StateNull); err != nil {
		return err
	}

	src.mu.Lock()
	srcPad, sinkPad, err := removeGhostPads(src, b)
	src.mu.Unlock()
	if err != nil {
		return err
	}
	srcPad.Unlink(sinkPad.Pad)

	if err = b.bin.Remove(src.bin.Element); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	b.elements[0].ReleaseRequestPad(sinkPad.GetTarget())
	return nil
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

func (b *Bin) AddSinkBin(sink *Bin) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	sink.setErrorHandler(b.errHandler)
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
				if b.errHandler != nil {
					b.errHandler(errors.ErrPadLinkFailed(b.bin.GetName(), sink.bin.GetName(), padReturn.String()))
				}
				return gst.PadProbeUnhandled
			}

			sink.bin.SyncStateWithParent()
			return gst.PadProbeRemove
		})
	}

	return nil
}

func (b *Bin) RemoveSinkBin(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	var sink *Bin
	for i, s := range b.sinks {
		if s.bin.GetName() == name {
			sink = s
			b.sinks = append(b.sinks[:i], b.sinks[i+1:]...)
			break
		}
	}
	if sink == nil {
		return nil
	}

	if b.state != gst.StatePlaying {
		if err := b.bin.Remove(sink.bin.Element); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		return nil
	}

	sink.mu.Lock()
	srcPad, sinkPad, err := removeGhostPads(b, sink)
	sink.mu.Unlock()
	if err != nil {
		return err
	}

	srcPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		srcPad.Unlink(sinkPad.Pad)
		sinkPad.Pad.SendEvent(gst.NewEOSEvent())

		b.mu.Lock()
		err := b.bin.Remove(sink.bin.Element)
		b.mu.Unlock()
		if err != nil {
			if b.errHandler != nil {
				b.errHandler(errors.ErrGstPipelineError(err))
			}
			return gst.PadProbeRemove
		}

		if err = sink.downgradeState(gst.StateNull); err != nil {
			logger.Warnw(fmt.Sprintf("failed to change %s state", sink.bin.GetName()), err)
		}

		b.elements[len(b.elements)-1].ReleaseRequestPad(srcPad.GetTarget())
		return gst.PadProbeRemove
	})

	return nil
}

func (b *Bin) SetEOSFunc(f func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.eosFunc = f
}

func (b *Bin) SetFinalizer(f func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.finalizer = f
}

func (b *Bin) link() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// link srcs bins
	for _, src := range b.srcs {
		if err := src.link(); err != nil {
			return err
		}
	}

	// link elements
	if err := gst.ElementLinkMany(b.elements...); err != nil {
		return errors.ErrGstPipelineError(err)
	}

	// link sinks bins
	for _, sink := range b.sinks {
		if err := sink.link(); err != nil {
			return err
		}
	}

	// link srcs bins to b
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

	// link b to sinks bins
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

	return nil
}

// ----- Pads -----

func createGhostPads(src, sink *Bin) (*gst.GhostPad, *gst.GhostPad, error) {
	srcElement := src.elements[len(src.elements)-1]
	sinkElement := sink.elements[0]

	var srcPad, sinkPad *gst.GhostPad
	if src.dataType != DataTypeMuxed || sink.dataType == DataTypeMuxed {
		srcPad = src.createGhostPad(srcElement, "src", sink)
	} else {
		srcPad = src.createGhostPad(srcElement, string(sink.dataType), sink)
	}
	if sink.dataType != DataTypeMuxed || src.dataType == DataTypeMuxed {
		sinkPad = sink.createGhostPad(sinkElement, "sink", src)
	} else {
		sinkPad = sink.createGhostPad(sinkElement, string(src.dataType), src)
	}

	if srcPad == nil || sinkPad == nil {
		return nil, nil, errors.ErrGhostPadFailed
	}

	src.pads[sink.bin.GetName()] = srcPad
	sink.pads[src.bin.GetName()] = sinkPad
	return srcPad, sinkPad, nil
}

func (b *Bin) createGhostPad(e *gst.Element, padType string, other *Bin) *gst.GhostPad {
	if pad := e.GetStaticPad(padType); pad != nil {
		return gst.NewGhostPad(fmt.Sprintf("%s_%s", b.bin.GetName(), padType), pad)
	}
	if pad := e.GetRequestPad(fmt.Sprintf("%s_%%u", padType)); pad != nil {
		return gst.NewGhostPad(fmt.Sprintf("%s_%s_%s", b.bin.GetName(), padType, other.bin.GetName()), pad)
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

func (b *Bin) setErrorHandler(f func(error)) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.errHandler = f
	for _, src := range b.srcs {
		src.setErrorHandler(f)
	}
	for _, sink := range b.sinks {
		sink.setErrorHandler(f)
	}
}

// ----- State -----

func (b *Bin) upgradeState(state gst.State) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, sink := range b.sinks {
		if err := sink.upgradeState(state); err != nil {
			return err
		}
	}
	if b.state < state {
		if err := b.bin.BlockSetState(state); err != nil {
			return errors.ErrStateChangeFailed(b.bin.GetName(), state)
		}
	}
	for _, src := range b.srcs {
		if err := src.upgradeState(state); err != nil {
			return err
		}
	}

	b.state = state
	return nil
}

func (b *Bin) downgradeState(state gst.State) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, src := range b.srcs {
		if err := src.downgradeState(state); err != nil {
			return err
		}
	}
	if b.state > state {
		if err := b.bin.BlockSetState(state); err != nil {
			return errors.ErrStateChangeFailed(b.bin.GetName(), state)
		}
	}
	for _, sink := range b.sinks {
		if err := sink.downgradeState(state); err != nil {
			return err
		}
	}

	b.state = state
	return nil
}

// ----- Closing -----

func (b *Bin) onError(err error) {
	b.mu.Lock()
	f := b.errHandler
	b.mu.Unlock()

	if f != nil {
		f(err)
	}
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

func (b *Bin) finalize() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	errs := errors.ErrArray{}
	for _, src := range b.srcs {
		if err := src.finalize(); err != nil {
			errs.AppendErr(err)
		}
	}
	if err := b.finalize(); err != nil {
		errs.AppendErr(err)
	}
	for _, sink := range b.sinks {
		if err := sink.finalize(); err != nil {
			errs.AppendErr(err)
		}
	}
	return errs.ToError()
}
