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

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock drives the gate's PLI throttle deterministically.

type gateFrame struct {
	pts      time.Duration
	keyframe bool
}

// feedGate runs frames through the gate and returns which PTS were forwarded
// and how many keyframe requests it made.
func feedGate(g *captureGate, frames []gateFrame) (forwarded []time.Duration, plis int) {
	for _, f := range frames {
		fwd, pli := g.decide(f.pts, f.keyframe)
		if fwd {
			forwarded = append(forwarded, f.pts)
		}
		if pli {
			plis++
		}
	}
	return forwarded, plis
}

// stream30fps builds a 30 fps stream from start to end (exclusive) with
// keyframes at the given PTS.
func stream30fps(start, end time.Duration, keyframes ...time.Duration) []gateFrame {
	isKey := make(map[time.Duration]bool, len(keyframes))
	for _, k := range keyframes {
		isKey[k] = true
	}
	var frames []gateFrame
	for pts := start; pts < end; pts += 33 * time.Millisecond {
		frames = append(frames, gateFrame{pts: pts, keyframe: isKey[pts]})
	}
	return frames
}

func TestCaptureGate_DropsUntilFirstKeyframeAndRequestsPLI(t *testing.T) {
	g := newCaptureGate(5 * time.Second)

	forwarded, plis := feedGate(g, stream30fps(0, 990*time.Millisecond))

	require.Empty(t, forwarded, "nothing is decodable before the first keyframe")
	require.Equal(t, 1, plis, "one request; the app writer repeats it until a keyframe arrives")
}

func TestCaptureGate_ForwardsKeyframeAndTwoTrailingDeltas(t *testing.T) {
	g := newCaptureGate(5 * time.Second)

	forwarded, _ := feedGate(g, stream30fps(0, 3*time.Second, 0))

	require.Equal(t, []time.Duration{0, 33 * time.Millisecond, 66 * time.Millisecond}, forwarded,
		"the keyframe plus one delta per downstream videorate, each of which holds a frame until a later one arrives")
}

func TestCaptureGate_DropsKeyframesBeforeTheCaptureIsDue(t *testing.T) {
	g := newCaptureGate(5 * time.Second)

	// Publisher with a 2 s GOP: keyframes at 0, 1.98 s and 3.96 s. The second
	// and third arrive before the next capture is due at 5 s.
	forwarded, plis := feedGate(g, stream30fps(0, 4*time.Second,
		0, 1980*time.Millisecond, 3960*time.Millisecond))

	require.Equal(t, []time.Duration{0, 33 * time.Millisecond, 66 * time.Millisecond}, forwarded)
	require.Zero(t, plis, "a keyframe already captured needs no PLI until the next capture is due")
}

func TestCaptureGate_DropsKeyframeJustBeforeDueTime(t *testing.T) {
	g := newCaptureGate(5 * time.Second)

	// A keyframe 380 ms before the 5 s capture must not be taken: the image
	// videorate picks the frame nearest its 5 s slot and only releases it once
	// a later frame arrives, so a frame before the slot waits a whole interval
	// for the next capture to push it out.
	early := 4620 * time.Millisecond
	late := 5280 * time.Millisecond
	forwarded, plis := feedGate(g, stream30fps(0, 5500*time.Millisecond, 0, early, late))

	require.NotContains(t, forwarded, early)
	require.Equal(t, []time.Duration{0, 33 * time.Millisecond, 66 * time.Millisecond,
		late, late + 33*time.Millisecond, late + 66*time.Millisecond}, forwarded)
	require.Equal(t, 1, plis, "the capture fell due at 5 s with only deltas arriving, so one PLI went out")
}

func TestCaptureGate_RequestsPLIWhenCaptureIsDueWithoutKeyframe(t *testing.T) {
	g := newCaptureGate(5 * time.Second)

	// A WebRTC publisher sends no periodic keyframes: only the first one.
	forwarded, plis := feedGate(g, stream30fps(0, 7*time.Second, 0))

	require.Equal(t, []time.Duration{0, 33 * time.Millisecond, 66 * time.Millisecond}, forwarded)
	// Due at 5 s: one request, which the app writer repeats until a keyframe arrives.
	require.Equal(t, 1, plis)
}

func TestCaptureGate_RequestsAgainForTheNextCapture(t *testing.T) {
	g := newCaptureGate(5 * time.Second)

	// The requested keyframe for the 5 s capture arrives at 5.28 s; the 10 s
	// capture needs a request of its own.
	forwarded, plis := feedGate(g, stream30fps(0, 10500*time.Millisecond, 0, 5280*time.Millisecond))

	require.Contains(t, forwarded, 5280*time.Millisecond)
	require.Equal(t, 2, plis, "one request per due capture")
}

func TestCaptureGate_CaptureScheduleDoesNotDrift(t *testing.T) {
	g := newCaptureGate(5 * time.Second)

	// The requested keyframe arrives 280 ms after it fell due. The next
	// capture must still fall due at 10 s, not 280 ms later.
	k2 := 5280 * time.Millisecond  // 5 s + 280 ms (on the 33 ms grid)
	k3 := 10032 * time.Millisecond // after 10 s, but before 10.28 s
	forwarded, _ := feedGate(g, stream30fps(0, 10200*time.Millisecond, 0, k2, k3))

	require.Contains(t, forwarded, k3, "the third capture falls due at 10 s regardless of when the second keyframe arrived")
}

func TestCaptureGate_SkipsMissedSlotsAfterALongGap(t *testing.T) {
	g := newCaptureGate(5 * time.Second)

	feedGate(g, stream30fps(0, 100*time.Millisecond, 0))

	// The track stalls and the next keyframe arrives at 17 s: it is captured,
	// and the next capture falls due at 20 s, not 10 s or 15 s.
	fwd, _ := g.decide(17*time.Second, true)
	require.True(t, fwd)
	fwd, _ = g.decide(17*time.Second+33*time.Millisecond, false)
	require.True(t, fwd, "first trailing delta")
	fwd, _ = g.decide(17*time.Second+66*time.Millisecond, false)
	require.True(t, fwd, "second trailing delta")
	fwd, _ = g.decide(17*time.Second+99*time.Millisecond, false)
	require.False(t, fwd, "no more than two trailing deltas")

	fwd, _ = g.decide(19500*time.Millisecond, true)
	require.False(t, fwd, "19.5 s is before the capture due at 20 s")
	fwd, _ = g.decide(20*time.Second, true)
	require.True(t, fwd)
}

func TestCaptureGate_RestartsOnPTSJumpBackwards(t *testing.T) {
	g := newCaptureGate(5 * time.Second)

	feedGate(g, stream30fps(100*time.Second, 100*time.Second+100*time.Millisecond, 100*time.Second))

	// A new segment restarts timestamps near zero. Waiting for 104.5 s would
	// starve the egress forever, so the gate starts over.
	fwd, _ := g.decide(2*time.Second, true)
	require.True(t, fwd)
}

func TestCaptureGate_RestartsOnPTSJumpBackwardsEarly(t *testing.T) {
	g := newCaptureGate(5 * time.Second)

	// Captured at 3 s, next due at 8 s. PTS then restarts near zero, within
	// the first two intervals: the keyframe at 1 s must start over instead of
	// waiting for 8 s.
	feedGate(g, stream30fps(3*time.Second, 3*time.Second+100*time.Millisecond, 3*time.Second))

	fwd, _ := g.decide(time.Second, true)
	require.True(t, fwd)
}

func TestCaptureGate_SmallPTSReorderDoesNotRestart(t *testing.T) {
	g := newCaptureGate(5 * time.Second)

	feedGate(g, stream30fps(0, 200*time.Millisecond, 0))

	// A keyframe slightly behind the last PTS is reordering, not a restart:
	// the next capture is still due at 5 s.
	fwd, _ := g.decide(100*time.Millisecond, true)
	require.False(t, fwd)
}
