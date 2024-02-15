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
}

// A pipeline can have either elements or src and sink bins. If you add both you will get a wrong hierarchy error
// Bins can contain both elements and src and sink bins
func NewPipeline(name string, latency uint64, callbacks *Callbacks) (*Pipeline, error) {
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
	if p.elementsAdded {
		return errors.ErrWrongHierarchy
	}
	p.binsAdded = true
	return p.Bin.AddSourceBin(src)
}

func (p *Pipeline) AddSinkBin(sink *Bin) error {
	if p.elementsAdded {
		return errors.ErrWrongHierarchy
	}
	p.binsAdded = true
	return p.Bin.AddSinkBin(sink)
}

func (p *Pipeline) AddElement(e *gst.Element) error {
	if p.binsAdded {
		return errors.ErrWrongHierarchy
	}
	p.elementsAdded = true
	return p.Bin.AddElement(e)
}

func (p *Pipeline) AddElements(elements ...*gst.Element) error {
	if p.binsAdded {
		return errors.ErrWrongHierarchy
	}
	p.elementsAdded = true
	return p.Bin.AddElements(elements...)
}

func (p *Pipeline) Link() error {
	return p.link()
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
	if _, ok := p.UpgradeState(StateStarted); ok {
		if err := p.SetState(gst.StatePlaying); err != nil {
			return err
		}
		if _, ok = p.UpgradeState(StateRunning); ok {
			p.loop.Run()
		}
	}

	return nil
}

func (p *Pipeline) SendEOS() {
	old, ok := p.UpgradeState(StateEOS)
	if ok {
		if old >= StateRunning {
			p.sendEOS()
		} else {
			p.Stop()
		}
	}
}

func (p *Pipeline) Stop() {
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
