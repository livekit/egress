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
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

// newTestProbe constructs a keyframeProbe without attaching any real pad probe,
// so processBuffer can be exercised directly with synthetic buffers.
func newTestProbe(mimeType types.MimeType, onRequestPLI func()) *keyframeProbe {
	p := &keyframeProbe{
		trackID:      "test-track",
		mimeType:     mimeType,
		onRequestPLI: onRequestPLI,
		logger:       logger.GetLogger().WithValues("trackID", "test-track", "component", "keyframe_probe_test"),
		keyframePTS:  make([]time.Duration, 0, keyframeHistorySize),
	}
	p.keyframePending = true
	return p
}

// makeBuffer constructs an empty gst.Buffer with the given PTS (or
// ClockTimeNone when ptsNone is true), and sets BufferFlagDeltaUnit for
// non-keyframes.
func makeBuffer(t *testing.T, ptsNone bool, pts time.Duration, isKeyframe bool) *gst.Buffer {
	t.Helper()
	buf := gst.NewEmptyBuffer()
	require.NotNil(t, buf)
	if ptsNone {
		buf.SetPresentationTimestamp(gst.ClockTimeNone)
	} else {
		buf.SetPresentationTimestamp(gst.ClockTime(uint64(pts)))
	}
	if !isKeyframe {
		buf.SetFlags(buf.GetFlags() | gst.BufferFlagDeltaUnit)
	}
	return buf
}

func TestKeyframeProbe_DropsPreKeyframePFrameAndRequestsPLI(t *testing.T) {
	initGStreamer(t)

	var pliCount atomic.Int32
	p := newTestProbe(types.MimeTypeH264, func() { pliCount.Add(1) })

	buf := makeBuffer(t, false, 10*time.Millisecond, false)
	require.Equal(t, gst.PadProbeDrop, p.processBuffer(buf))
	require.Equal(t, int32(1), pliCount.Load())
}

func TestKeyframeProbe_ThrottlesPLIRequestsInBurst(t *testing.T) {
	initGStreamer(t)

	var pliCount atomic.Int32
	p := newTestProbe(types.MimeTypeH264, func() { pliCount.Add(1) })

	for i := 0; i < 5; i++ {
		buf := makeBuffer(t, false, time.Duration(i)*time.Millisecond, false)
		require.Equal(t, gst.PadProbeDrop, p.processBuffer(buf))
	}
	require.Equal(t, int32(1), pliCount.Load(), "burst within throttle window should yield a single PLI")
}

func TestKeyframeProbe_KeyframeAfterPendingIsForwarded(t *testing.T) {
	initGStreamer(t)

	var pliCount atomic.Int32
	p := newTestProbe(types.MimeTypeH264, func() { pliCount.Add(1) })

	// Establish pending state via a pre-keyframe P-frame.
	pBuf := makeBuffer(t, false, 5*time.Millisecond, false)
	require.Equal(t, gst.PadProbeDrop, p.processBuffer(pBuf))

	// Keyframe arrives — must be forwarded.
	kBuf := makeBuffer(t, false, 10*time.Millisecond, true)
	require.Equal(t, gst.PadProbeOK, p.processBuffer(kBuf))
	require.False(t, kBuf.HasFlags(gst.BufferFlagDiscont), "probe should not set DISCONT; rely on segment events for discontinuity signaling")
}

func TestKeyframeProbe_PostKeyframeDeltaUnitForwardedWithoutPLI(t *testing.T) {
	initGStreamer(t)

	var pliCount atomic.Int32
	p := newTestProbe(types.MimeTypeH264, func() { pliCount.Add(1) })

	kBuf := makeBuffer(t, false, 10*time.Millisecond, true)
	require.Equal(t, gst.PadProbeOK, p.processBuffer(kBuf))

	pliBefore := pliCount.Load()

	for i := 0; i < 5; i++ {
		buf := makeBuffer(t, false, time.Duration(20+i)*time.Millisecond, false)
		require.Equal(t, gst.PadProbeOK, p.processBuffer(buf))
	}
	require.Equal(t, pliBefore, pliCount.Load(), "post-keyframe P-frames should not request PLI")
}

func TestKeyframeProbe_MissingPTSWithoutPriorValidDrops(t *testing.T) {
	initGStreamer(t)

	var pliCount atomic.Int32
	p := newTestProbe(types.MimeTypeVP9, func() { pliCount.Add(1) })

	// No prior valid PTS to restore from — drop, and don't request a PLI
	// (missing-PTS is a parser hiccup, not a no-keyframe signal).
	buf := makeBuffer(t, true, 0, false)
	require.Equal(t, gst.PadProbeDrop, p.processBuffer(buf))
	require.Equal(t, int32(0), pliCount.Load())
}

func TestKeyframeProbe_MissingPTSPostKeyframeRestoresPTSAndForwards(t *testing.T) {
	initGStreamer(t)

	var pliCount atomic.Int32
	p := newTestProbe(types.MimeTypeVP9, func() { pliCount.Add(1) })

	// Establish a prior valid PTS via a keyframe (also clears keyframePending).
	lastPTS := 50 * time.Millisecond
	kBuf := makeBuffer(t, false, lastPTS, true)
	require.Equal(t, gst.PadProbeOK, p.processBuffer(kBuf))
	pliBefore := pliCount.Load()

	// Missing-PTS P-frame post-keyframe — PTS patched from lastSrcPTS, buffer
	// continues, no PLI (we know re-timestamping fixes the underlying issue).
	mBuf := makeBuffer(t, true, 0, false)
	require.Equal(t, gst.PadProbeOK, p.processBuffer(mBuf))
	require.Equal(t, gst.ClockTime(uint64(lastPTS)), mBuf.PresentationTimestamp(), "PTS should be restored from last good value")
	require.Equal(t, pliBefore, pliCount.Load(), "missing-PTS should not request PLI")
}
