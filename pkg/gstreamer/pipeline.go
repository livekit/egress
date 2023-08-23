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
	"github.com/frostbyte73/core"
	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
)

type Pipeline struct {
	*Bin

	loop *glib.MainLoop

	started core.Fuse
	running chan struct{}
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
			Callbacks: callbacks,
			pipeline:  pipeline,
			bin:       pipeline.Bin,
			latency:   latency,
			queues:    make(map[string]*gst.Element),
		},
		loop:    glib.NewMainLoop(glib.MainContextDefault(), false),
		started: core.NewFuse(),
		running: make(chan struct{}),
	}, nil
}

func (p *Pipeline) Link() error {
	return p.link()
}

func (p *Pipeline) SetWatch(watch func(msg *gst.Message) bool) {
	p.pipeline.GetPipelineBus().AddWatch(watch)
}

func (p *Pipeline) SetState(state gst.State) error {
	if err := p.pipeline.SetState(state); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	return nil
}

func (p *Pipeline) Run() error {
	p.started.Once(func() {
		if err := p.SetState(gst.StatePlaying); err != nil {
			p.OnError(err)
			return
		}
		logger.Infow("running")
		p.loop.Run()
		close(p.running)
	})

	// wait
	<-p.running
	return nil
}

func (p *Pipeline) SendEOS() {
	p.sendEOS()
}

func (p *Pipeline) Stop() {
	defer p.loop.Quit()
	if err := p.SetState(gst.StateNull); err != nil {
		p.OnError(err)
		return
	}
	if err := p.OnStop(); err != nil {
		p.OnError(err)
		return
	}
}

func (p *Pipeline) DebugBinToDotData(details gst.DebugGraphDetails) string {
	return p.pipeline.DebugBinToDotData(details)
}
