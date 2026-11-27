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
	"github.com/linkdata/deadlock"
	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

type Callbacks struct {
	mu         deadlock.RWMutex
	GstReady   chan struct{}
	BuildReady chan struct{}

	// upstream callbacks
	onError           func(error)
	onStop            []func() error
	onDebugDotRequest func(string)

	// source callbacks
	onTrackAdded            []func(*config.TrackSource)
	onTrackMuted            []func(string)
	onTrackUnmuted          []func(string)
	onTrackRemoved          []func(string)
	onActiveSpeakersChanged []func([]lksdk.Participant)
	onEOSSent               func()
	onDataFrame             func(types.DataFrame) error
	onDataTrackEnded        func(string)

	// held across delivery so a backlog replay can't be overtaken by a live event
	roomEventMu      deadlock.Mutex
	onRoomEvent      []func(types.RoomEvent)
	roomEventBacklog []types.RoomEvent

	pipelinePaused   core.Fuse
	pipelineStopping core.Fuse
}

func (c *Callbacks) SetOnError(f func(error)) {
	c.mu.Lock()
	c.onError = f
	c.mu.Unlock()
}

func (c *Callbacks) OnError(err error) {
	c.mu.RLock()
	onError := c.onError
	c.mu.RUnlock()

	if onError != nil {
		onError(err)
	}
}

func (c *Callbacks) SetOnDebugDotRequest(f func(string)) {
	c.mu.Lock()
	c.onDebugDotRequest = f
	c.mu.Unlock()
}

func (c *Callbacks) OnDebugDotRequest(reason string) {
	c.mu.RLock()
	onDebugDotRequest := c.onDebugDotRequest
	c.mu.RUnlock()

	if onDebugDotRequest != nil {
		onDebugDotRequest(reason)
	}
}

func (c *Callbacks) PipelinePaused() <-chan struct{} {
	return c.pipelinePaused.Watch()
}

func (c *Callbacks) OnPipelinePaused() {
	c.pipelinePaused.Break()
}

func (c *Callbacks) AddOnStop(f func() error) {
	c.mu.Lock()
	c.onStop = append(c.onStop, f)
	c.mu.Unlock()
}

// PipelineStopping reports whether Pipeline.Stop has begun tearing the pipeline down.
func (c *Callbacks) PipelineStopping() bool {
	return c.pipelineStopping.IsBroken()
}

func (c *Callbacks) OnStop() error {
	c.pipelineStopping.Break()

	c.mu.RLock()
	onStop := c.onStop
	c.mu.RUnlock()

	errArray := &errors.ErrArray{}
	for _, f := range onStop {
		errArray.Check(f())
	}
	return errArray.ToError()
}

func (c *Callbacks) AddOnTrackAdded(f func(*config.TrackSource)) {
	c.mu.Lock()
	c.onTrackAdded = append(c.onTrackAdded, f)
	c.mu.Unlock()
}

func (c *Callbacks) OnTrackAdded(ts *config.TrackSource) {
	c.mu.RLock()
	onTrackAdded := c.onTrackAdded
	c.mu.RUnlock()

	for _, f := range onTrackAdded {
		f(ts)
	}
}

func (c *Callbacks) AddOnTrackMuted(f func(string)) {
	c.mu.Lock()
	c.onTrackMuted = append(c.onTrackMuted, f)
	c.mu.Unlock()
}

func (c *Callbacks) OnTrackMuted(trackID string) {
	c.mu.RLock()
	onTrackMuted := c.onTrackMuted
	c.mu.RUnlock()

	for _, f := range onTrackMuted {
		f(trackID)
	}
}

func (c *Callbacks) AddOnTrackUnmuted(f func(string)) {
	c.mu.Lock()
	c.onTrackUnmuted = append(c.onTrackUnmuted, f)
	c.mu.Unlock()
}

func (c *Callbacks) OnTrackUnmuted(trackID string) {
	c.mu.RLock()
	onTrackUnmuted := c.onTrackUnmuted
	c.mu.RUnlock()

	for _, f := range onTrackUnmuted {
		f(trackID)
	}
}

func (c *Callbacks) AddOnTrackRemoved(f func(string)) {
	c.mu.Lock()
	c.onTrackRemoved = append(c.onTrackRemoved, f)
	c.mu.Unlock()
}

func (c *Callbacks) OnTrackRemoved(trackID string) {
	c.mu.RLock()
	onTrackRemoved := c.onTrackRemoved
	c.mu.RUnlock()

	for _, f := range onTrackRemoved {
		f(trackID)
	}
}

func (c *Callbacks) AddOnActiveSpeakersChanged(f func([]lksdk.Participant)) {
	c.mu.Lock()
	c.onActiveSpeakersChanged = append(c.onActiveSpeakersChanged, f)
	c.mu.Unlock()
}

func (c *Callbacks) OnActiveSpeakersChanged(speakers []lksdk.Participant) {
	c.mu.RLock()
	handlers := c.onActiveSpeakersChanged
	c.mu.RUnlock()

	for _, f := range handlers {
		f(speakers)
	}
}

// AddOnRoomEvent registers f and replays to it every room event raised before the first handler was added
func (c *Callbacks) AddOnRoomEvent(f func(types.RoomEvent)) {
	c.roomEventMu.Lock()
	defer c.roomEventMu.Unlock()

	for _, ev := range c.roomEventBacklog {
		f(ev)
	}
	c.roomEventBacklog = nil
	c.onRoomEvent = append(c.onRoomEvent, f)
}

func (c *Callbacks) OnRoomEvent(ev types.RoomEvent) {
	c.roomEventMu.Lock()
	defer c.roomEventMu.Unlock()

	if len(c.onRoomEvent) == 0 {
		c.roomEventBacklog = append(c.roomEventBacklog, ev)
		return
	}
	for _, f := range c.onRoomEvent {
		f(ev)
	}
}

// SetOnDataFrame sets the data track frame handler; frames raised before it is set are dropped
func (c *Callbacks) SetOnDataFrame(f func(types.DataFrame) error) {
	c.mu.Lock()
	c.onDataFrame = f
	c.mu.Unlock()
}

func (c *Callbacks) OnDataFrame(frame types.DataFrame) error {
	c.mu.RLock()
	onDataFrame := c.onDataFrame
	c.mu.RUnlock()

	if onDataFrame != nil {
		return onDataFrame(frame)
	}
	return nil
}

func (c *Callbacks) SetOnDataTrackEnded(f func(string)) {
	c.mu.Lock()
	c.onDataTrackEnded = f
	c.mu.Unlock()
}

func (c *Callbacks) OnDataTrackEnded(trackID string) {
	c.mu.RLock()
	onDataTrackEnded := c.onDataTrackEnded
	c.mu.RUnlock()

	if onDataTrackEnded != nil {
		onDataTrackEnded(trackID)
	}
}

func (c *Callbacks) SetOnEOSSent(f func()) {
	c.mu.Lock()
	c.onEOSSent = f
	c.mu.Unlock()
}

func (c *Callbacks) OnEOSSent() {
	c.mu.RLock()
	onEOSSent := c.onEOSSent
	c.mu.RUnlock()

	if onEOSSent != nil {
		onEOSSent()
	}
}
