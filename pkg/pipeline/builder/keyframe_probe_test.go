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
func newTestProbe(mimeType types.MimeType, onKeyframeReceived func()) *keyframeProbe {
	p := &keyframeProbe{
		trackID:            "test-track",
		mimeType:           mimeType,
		onKeyframeReceived: onKeyframeReceived,
		logger:             logger.GetLogger().WithValues("trackID", "test-track", "component", "keyframe_probe_test"),
		keyframePTS:        make([]time.Duration, 0, keyframeHistorySize),
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

func TestKeyframeProbe_DropsPreKeyframePFrames(t *testing.T) {
	initGStreamer(t)

	var received atomic.Int32
	p := newTestProbe(types.MimeTypeH264, func() { received.Add(1) })

	for i := 0; i < 5; i++ {
		buf := makeBuffer(t, false, time.Duration(i)*time.Millisecond, false)
		require.Equal(t, gst.PadProbeDrop, p.processBuffer(buf))
	}
	require.Equal(t, int32(0), received.Load())
}

func TestKeyframeProbe_FirstKeyframeIsForwardedAndReported(t *testing.T) {
	initGStreamer(t)

	var received atomic.Int32
	p := newTestProbe(types.MimeTypeH264, func() { received.Add(1) })

	pBuf := makeBuffer(t, false, 5*time.Millisecond, false)
	require.Equal(t, gst.PadProbeDrop, p.processBuffer(pBuf))

	kBuf := makeBuffer(t, false, 10*time.Millisecond, true)
	require.Equal(t, gst.PadProbeOK, p.processBuffer(kBuf))
	require.False(t, kBuf.HasFlags(gst.BufferFlagDiscont), "probe should not set DISCONT; rely on segment events for discontinuity signaling")
	require.Equal(t, int32(1), received.Load())

	// later keyframes are not reported again
	require.Equal(t, gst.PadProbeOK, p.processBuffer(makeBuffer(t, false, 20*time.Millisecond, true)))
	require.Equal(t, int32(1), received.Load())
}

func TestKeyframeProbe_PostKeyframeDeltaUnitForwarded(t *testing.T) {
	initGStreamer(t)

	p := newTestProbe(types.MimeTypeH264, nil)

	kBuf := makeBuffer(t, false, 10*time.Millisecond, true)
	require.Equal(t, gst.PadProbeOK, p.processBuffer(kBuf))

	for i := 0; i < 5; i++ {
		buf := makeBuffer(t, false, time.Duration(20+i)*time.Millisecond, false)
		require.Equal(t, gst.PadProbeOK, p.processBuffer(buf))
	}
}

func TestKeyframeProbe_MissingPTSWithoutPriorValidDrops(t *testing.T) {
	initGStreamer(t)

	p := newTestProbe(types.MimeTypeVP9, nil)

	// No prior valid PTS to restore from — drop.
	buf := makeBuffer(t, true, 0, false)
	require.Equal(t, gst.PadProbeDrop, p.processBuffer(buf))
}

func TestKeyframeProbe_MissingPTSPostKeyframeRestoresPTSAndForwards(t *testing.T) {
	initGStreamer(t)

	p := newTestProbe(types.MimeTypeVP9, nil)

	// Establish a prior valid PTS via a keyframe (also clears keyframePending).
	lastPTS := 50 * time.Millisecond
	kBuf := makeBuffer(t, false, lastPTS, true)
	require.Equal(t, gst.PadProbeOK, p.processBuffer(kBuf))

	// Missing-PTS P-frame post-keyframe — PTS patched from lastSrcPTS, buffer
	// continues.
	mBuf := makeBuffer(t, true, 0, false)
	require.Equal(t, gst.PadProbeOK, p.processBuffer(mBuf))
	require.Equal(t, gst.ClockTime(uint64(lastPTS)), mBuf.PresentationTimestamp(), "PTS should be restored from last good value")
}
