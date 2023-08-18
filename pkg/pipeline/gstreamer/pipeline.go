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
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
)

type PipelineState string

type Pipeline struct {
	*Bin

	pipeline *gst.Pipeline
	loop     *glib.MainLoop

	stateLock    sync.Mutex
	pendingState gst.State

	started  core.Fuse
	running  chan struct{}
	eosSent  core.Fuse
	stopped  core.Fuse
	eosTimer *time.Timer
}

func NewPipeline() (*Pipeline, error) {
	pipeline, err := gst.NewPipeline("pipeline")
	if err != nil {
		return nil, err
	}

	return &Pipeline{
		Bin: &Bin{
			state: gst.StateNull,
		},
		pipeline: pipeline,
		loop:     glib.NewMainLoop(glib.MainContextDefault(), false),
		started:  core.NewFuse(),
		running:  make(chan struct{}),
		eosSent:  core.NewFuse(),
		stopped:  core.NewFuse(),
	}, nil
}

func (p *Pipeline) SetWatch(watch func(msg *gst.Message) bool) {
	p.pipeline.GetPipelineBus().AddWatch(watch)
}

func (p *Pipeline) SetErrorHandler(f func(error)) {
	p.setErrorHandler(f)
}

func (p *Pipeline) Link() error {
	return p.link()
}

func (p *Pipeline) Run() error {
	p.started.Once(func() {
		go func() {
			p.loop.Run()
			close(p.running)
		}()
	})

	if err := p.setState(gst.StatePlaying); err != nil {
		return err
	}

	<-p.running
	return nil
}

func (p *Pipeline) SendEOS(timeout time.Duration) {
	p.eosSent.Once(func() {
		p.eosTimer = time.AfterFunc(timeout, func() {
			p.onError(errors.ErrPipelineFrozen)
		})

		p.sendEOS()
	})
}

func (p *Pipeline) Stop() {
	p.stopped.Once(func() {
		p.mu.Lock()
		onError := p.errHandler
		if p.eosTimer != nil {
			p.eosTimer.Stop()
			p.eosTimer = nil
		}
		p.mu.Unlock()

		if err := p.setState(gst.StateNull); err != nil && onError != nil {
			onError(err)
		}
		if err := p.finalize(); err != nil && onError != nil {
			onError(err)
		}
	})
}

func (p *Pipeline) setState(state gst.State) error {
	p.stateLock.Lock()
	p.pendingState = state
	p.stateLock.Unlock()

	for {
		p.stateLock.Lock()

		switch {
		case p.pendingState != state || p.state == p.pendingState:
			p.stateLock.Unlock()
			return nil

		case p.state < p.pendingState:
			nextState := p.state + 1
			if err := p.upgradeState(nextState); err != nil {
				return err
			}
			p.state = nextState

		case p.state > p.pendingState:
			nextState := p.state - 1
			if err := p.downgradeState(nextState); err != nil {
				return err
			}
			p.state = nextState
		}

		p.stateLock.Unlock()
	}
}
