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

package builder

import "time"

const (
	// captureTrailingDeltas is how many delta frames follow each captured
	// keyframe to the decoder: one per videorate between the decoder and the
	// image encoder (the video bin's output rate and the image bin's capture
	// rate). Each holds its newest frame until a later one arrives, so with
	// fewer the capture would wait a whole interval for the next keyframe.
	captureTrailingDeltas = 2
	// captureRestartJump is how far PTS may go backwards before the gate treats
	// the stream as restarted. Frame reordering moves PTS by far less.
	captureRestartJump = time.Second
)

// captureGate decides which encoded frames an image-only egress decodes.
//
// Image outputs keep one frame per capture interval, so decoding every frame
// is wasted work. The gate forwards one keyframe per interval plus the
// captureTrailingDeltas delta frames right after it, and drops everything else
// before the decoder.
//
// Captures follow a fixed schedule (nextDue advances by whole intervals) so
// the delay of each requested keyframe does not accumulate into drift. Only a
// keyframe at or after the due time is taken: the image bin's videorate picks
// the frame nearest each capture slot, and a frame just before the slot would
// stay held until the next capture. When a capture is due and only delta
// frames arrive, the gate asks for a keyframe once; the app writer repeats
// the request until one arrives.
//
// It is not safe for concurrent use; the keyframe probe calls it from a single
// streaming thread.
type captureGate struct {
	interval time.Duration

	started   bool
	lastPTS   time.Duration
	nextDue   time.Duration
	trailing  int  // delta frames still to forward after the last capture
	requested bool // a keyframe was requested for the pending capture
}

func newCaptureGate(interval time.Duration) *captureGate {
	return &captureGate{interval: interval}
}

// decide reports whether the frame at pts should reach the decoder, and
// whether a keyframe should be requested from the publisher.
func (g *captureGate) decide(pts time.Duration, isKeyframe bool) (forward, requestKeyframe bool) {
	// A restarted stream would otherwise wait for the old schedule.
	if g.started && pts+captureRestartJump < g.lastPTS {
		g.started = false
	}
	g.lastPTS = pts

	if !g.started {
		if isKeyframe {
			g.capture(pts)
			return true, false
		}
		return false, g.requestOnce()
	}

	due := pts >= g.nextDue
	switch {
	case isKeyframe && due:
		g.capture(pts)
		return true, false
	case !isKeyframe && g.trailing > 0:
		g.trailing--
		return true, false
	case !isKeyframe && due:
		return false, g.requestOnce()
	default:
		return false, false
	}
}

func (g *captureGate) capture(pts time.Duration) {
	if !g.started {
		g.started = true
		g.nextDue = pts
	}
	for g.nextDue <= pts {
		g.nextDue += g.interval
	}
	g.trailing = captureTrailingDeltas
	g.requested = false
}

func (g *captureGate) requestOnce() bool {
	if g.requested {
		return false
	}
	g.requested = true
	return true
}
