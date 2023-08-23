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

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
)

type Callbacks struct {
	mu       sync.RWMutex
	GstReady chan struct{}

	// upstream callbacks
	onError func(error)
	onStop  []func() error

	// source callbacks
	onTrackAdded   []func(*config.TrackSource)
	onTrackMuted   []func(string)
	onTrackUnmuted []func(string, time.Duration)
	onTrackRemoved []func(string)

	// internal
	addBin    func(bin *gst.Bin)
	removeBin func(bin *gst.Bin)
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

func (c *Callbacks) AddOnStop(f func() error) {
	c.mu.Lock()
	c.onStop = append(c.onStop, f)
	c.mu.Unlock()
}

func (c *Callbacks) OnStop() error {
	errArray := &errors.ErrArray{}
	c.mu.RLock()
	for _, onStop := range c.onStop {
		errArray.Check(onStop())
	}
	c.mu.RUnlock()
	return errArray.ToError()
}

func (c *Callbacks) AddOnTrackAdded(f func(*config.TrackSource)) {
	c.mu.Lock()
	c.onTrackAdded = append(c.onTrackAdded, f)
	c.mu.Unlock()
}

func (c *Callbacks) OnTrackAdded(ts *config.TrackSource) {
	c.mu.RLock()
	for _, onTrackAdded := range c.onTrackAdded {
		onTrackAdded(ts)
	}
	c.mu.RUnlock()
}

func (c *Callbacks) AddOnTrackMuted(f func(string)) {
	c.mu.Lock()
	c.onTrackMuted = append(c.onTrackMuted, f)
	c.mu.Unlock()
}

func (c *Callbacks) OnTrackMuted(trackID string) {
	c.mu.RLock()
	for _, onTrackMuted := range c.onTrackMuted {
		onTrackMuted(trackID)
	}
	c.mu.RUnlock()
}

func (c *Callbacks) AddOnTrackUnmuted(f func(string, time.Duration)) {
	c.mu.Lock()
	c.onTrackUnmuted = append(c.onTrackUnmuted, f)
	c.mu.Unlock()
}

func (c *Callbacks) OnTrackUnmuted(trackID string, pts time.Duration) {
	c.mu.RLock()
	for _, onTrackUnmuted := range c.onTrackUnmuted {
		onTrackUnmuted(trackID, pts)
	}
	c.mu.RUnlock()
}

func (c *Callbacks) AddOnTrackRemoved(f func(string)) {
	c.mu.Lock()
	c.onTrackRemoved = append(c.onTrackRemoved, f)
	c.mu.Unlock()
}

func (c *Callbacks) OnTrackRemoved(trackID string) {
	c.mu.RLock()
	for _, onTrackRemoved := range c.onTrackRemoved {
		onTrackRemoved(trackID)
	}
	c.mu.RUnlock()
}
