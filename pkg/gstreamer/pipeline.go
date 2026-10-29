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
	"math"
	"slices"
	"sync"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
)

const (
	stateChangeTimeout = time.Second * 15
)

type Pipeline struct {
	*Bin

	loop          *glib.MainLoop
	binsAdded     bool
	elementsAdded bool
	siblings      []*Bin // source-through-sink bins sharing only the bus and clock
	linked        bool
}

// A pipeline can have either elements or src and sink bins. If you add both you will get a wrong hierarchy error
// Bins can contain both elements and src and sink bins

func NewPipeline(name string, latency time.Duration, callbacks *Callbacks) (*Pipeline, error) {
	pipeline, err := gst.NewPipeline(name)
	if err != nil {
		return nil, err
	}

	return &Pipeline{
		Bin: &Bin{
			Callbacks:    callbacks,
			StateManager: &StateManager{},
			pipeline:     pipeline,
			bin:          pipeline.Bin,
			latency:      latency,
			queues:       make(map[string]*gst.Element),
		},
		loop: glib.NewMainLoop(glib.MainContextDefault(), false),
	}, nil
}

func (p *Pipeline) AddSourceBin(src *Bin) error {
	if err := p.claimHierarchy(true); err != nil {
		return err
	}
	return p.Bin.AddSourceBin(src)
}

func (p *Pipeline) AddSinkBin(sink *Bin) error {
	if err := p.claimHierarchy(true); err != nil {
		return err
	}
	return p.Bin.AddSinkBin(sink)
}

// AddSiblingBin adds a self-contained bin that is never linked to the pipeline's other bins; false once the pipeline is past running
func (p *Pipeline) AddSiblingBin(sibling *Bin) (bool, error) {
	if err := p.claimHierarchy(true); err != nil {
		return false, err
	}
	logger.Debugw(fmt.Sprintf("adding sibling %s", sibling.bin.GetName()))

	sibling.mu.Lock()
	alreadyAdded := sibling.added
	sibling.added = true
	sibling.mu.Unlock()
	if alreadyAdded {
		return false, errors.ErrBinAlreadyAdded
	}

	p.LockStateShared()
	if p.GetStateLocked() > StateRunning {
		p.UnlockStateShared()
		return false, nil
	}

	p.mu.Lock()
	if err := p.pipeline.Add(sibling.bin.Element); err != nil {
		p.mu.Unlock()
		p.UnlockStateShared()
		return false, errors.ErrGstPipelineError(err)
	}
	var err error
	if p.linked {
		err = sibling.link()
		if err == nil && !sibling.bin.SyncStateWithParent() {
			err = errors.ErrGstPipelineError(fmt.Errorf("failed to sync %s state with parent", sibling.bin.GetName()))
		}
	}
	if err == nil {
		p.siblings = append(p.siblings, sibling)
	}
	p.mu.Unlock()
	p.UnlockStateShared()

	if err != nil {
		// unlocked first: stopping a partly started sibling waits on its streaming thread
		_ = sibling.bin.SetState(gst.StateNull)
		_ = p.pipeline.Remove(sibling.bin.Element)
		return false, err
	}
	return true, nil
}

// RemoveSiblingBin stops and removes a bin added with AddSiblingBin, including while the pipeline drains to EOS
func (p *Pipeline) RemoveSiblingBin(name string) error {
	logger.Debugw(fmt.Sprintf("removing sibling %s", name))

	p.LockStateShared()
	state := p.GetStateLocked()
	if state > StateEOS {
		p.UnlockStateShared()
		return nil
	}

	p.mu.Lock()
	var sibling *Bin
	for i, s := range p.siblings {
		if s.bin.GetName() == name {
			sibling = s
			p.siblings = append(p.siblings[:i], p.siblings[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
	p.UnlockStateShared()
	if sibling == nil {
		return nil
	}

	remove := func() error {
		if err := sibling.bin.SetState(gst.StateNull); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		if err := p.pipeline.Remove(sibling.bin.Element); err != nil {
			return errors.ErrGstPipelineError(err)
		}
		return nil
	}
	// the GLib loop isn't running before StateRunning, so an idle removal would outlast the timeout below
	if state < StateRunning {
		return remove()
	}

	done := make(chan error, 1)
	if _, err := glib.IdleAdd(func() bool {
		done <- remove()
		return false
	}); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	select {
	case err := <-done:
		return err
	case <-time.After(removeSourceBinTimeout):
		// Stop quits the loop before the callback runs, and the pipeline going to NULL removes the need
		if p.GetState() > StateEOS {
			return nil
		}
		return errors.ErrPipelineFrozen
	}
}

// PostEOS posts EOS on the bus, for a pipeline with no sink left to post it
func (p *Pipeline) PostEOS() {
	if !p.pipeline.GetBus().Post(gst.NewEOSMessage(p.pipeline)) {
		logger.Warnw("failed to post EOS", nil)
	}
}

func (p *Pipeline) AddElement(e *gst.Element) error {
	if err := p.claimHierarchy(false); err != nil {
		return err
	}
	return p.Bin.AddElement(e)
}

func (p *Pipeline) AddElements(elements ...*gst.Element) error {
	if err := p.claimHierarchy(false); err != nil {
		return err
	}
	return p.Bin.AddElements(elements...)
}

func (p *Pipeline) Link() error {
	if err := p.link(); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, sibling := range p.siblings {
		if err := sibling.link(); err != nil {
			return err
		}
	}
	p.linked = true
	return nil
}

func (p *Pipeline) SetWatch(watch func(msg *gst.Message) bool) {
	p.pipeline.GetPipelineBus().AddWatch(watch)
}

func (p *Pipeline) SetState(state gst.State) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	stateErr := make(chan error, 1)
	go func() {
		stateErr <- p.pipeline.SetState(state)
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

func (p *Pipeline) Run() error {
	if err := p.SetState(gst.StatePlaying); err != nil {
		return err
	}
	if _, ok := p.UpgradeState(StateRunning); ok {
		p.loop.Run()
	}

	return nil
}

func (p *Pipeline) SendEOS() {
	old, ok := p.UpgradeState(StateEOS)
	if ok {
		if old >= StateRunning {
			p.sendEOS()

			p.mu.Lock()
			siblings := slices.Clone(p.siblings)
			p.mu.Unlock()
			var wg sync.WaitGroup
			for _, sibling := range siblings {
				wg.Go(sibling.sendEOS)
			}
			wg.Wait()
		} else {
			p.Stop()
		}
	}
}

func (p *Pipeline) Stop() {
	logger.Debugw("stopping pipeline")
	old, ok := p.UpgradeState(StateStopping)
	if !ok {
		return
	}

	if err := p.OnStop(); err != nil {
		p.OnError(err)
	}
	if err := p.SetState(gst.StateNull); err != nil {
		logger.Errorw("failed to set pipeline to null", err)
	}

	if old >= StateRunning {
		p.loop.Quit()
	}

	p.UpgradeState(StateFinished)
}

func (p *Pipeline) DebugBinToDotData(details gst.DebugGraphDetails) string {
	return p.pipeline.DebugBinToDotData(details)
}

// RunningTime returns the running time of the gst pipeline
func (p *Pipeline) RunningTime() (time.Duration, bool) {
	clock := p.pipeline.GetPipelineClock()
	if clock == nil {
		return 0, false
	}

	clockTime := clock.GetTime()
	if clockTime == gst.ClockTimeNone {
		return 0, false
	}

	baseTime := p.pipeline.GetBaseTime()
	if baseTime == gst.ClockTimeNone {
		return 0, false
	}

	clockValue := uint64(clockTime)
	baseValue := uint64(baseTime)
	if clockValue < baseValue {
		return 0, false
	}

	delta := clockValue - baseValue
	if delta > uint64(math.MaxInt64) {
		return time.Duration(math.MaxInt64), false
	}

	return time.Duration(int64(delta)), true
}

// PlayheadPosition returns the playhead position of the gst pipeline
// It is equivalent to the last timestamp seen by a sink element
func (p *Pipeline) PlayheadPosition() (time.Duration, bool) {
	ok, position := p.pipeline.QueryPosition(gst.FormatTime)
	if !ok || position < 0 {
		return 0, false
	}

	return time.Duration(position), true
}

// claimHierarchy commits the pipeline to holding bins or elements, never both; a failed add keeps the claim since it fails the egress
func (p *Pipeline) claimHierarchy(bins bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if (bins && p.elementsAdded) || (!bins && p.binsAdded) {
		return errors.ErrWrongHierarchy
	}
	if bins {
		p.binsAdded = true
	} else {
		p.elementsAdded = true
	}
	return nil
}
