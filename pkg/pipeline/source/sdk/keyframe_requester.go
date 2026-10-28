// Copyright 2026 LiveKit, Inc.
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

package sdk

import (
	"time"

	"github.com/frostbyte73/core"
	"go.uber.org/atomic"
)

// keyframeRequester sends throttled PLIs. While a keyframe is pending, each PLI
// queues the next one on the throttle, so requests repeat once per period until
// a keyframe arrives, even if nothing else asks for one. Without this, a parser
// that discards everything before the first keyframe leaves nobody to ask again
// once the throttle's last PLI is lost.
type keyframeRequester struct {
	throttle core.Throttle
	writePLI func()
	pending  atomic.Bool
	closed   atomic.Bool
}

func newKeyframeRequester(period time.Duration, writePLI func()) *keyframeRequester {
	return &keyframeRequester{
		throttle: core.NewThrottle(period),
		writePLI: writePLI,
	}
}

// RequestPLI sends a single throttled PLI.
func (r *keyframeRequester) RequestPLI() {
	if r.closed.Load() {
		return
	}
	r.throttle(r.send)
}

// RequestKeyframe sends PLIs until KeyframeReceived or Close is called.
func (r *keyframeRequester) RequestKeyframe() {
	r.pending.Store(true)
	r.RequestPLI()
}

func (r *keyframeRequester) KeyframeReceived() {
	r.pending.Store(false)
}

func (r *keyframeRequester) Close() {
	r.closed.Store(true)
}

func (r *keyframeRequester) send() {
	if r.closed.Load() {
		return
	}
	r.writePLI()
	if r.pending.Load() {
		r.throttle(r.send)
	}
}
