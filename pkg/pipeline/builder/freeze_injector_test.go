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

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

func newTestInjector(t *testing.T, fallbackActive func() bool) *freezeInjector {
	t.Helper()

	element, err := gst.NewElementWithName("appsrc", "test_freeze_src")
	require.NoError(t, err)
	src := app.SrcFromElement(element)

	return newFreezeInjector(src, 8*time.Second, fallbackActive)
}

func TestFreezeInjector_CacheStartsLoopOnce(t *testing.T) {
	initGStreamer(t)

	active := atomic.NewBool(false)
	f := newTestInjector(t, active.Load)
	defer f.Stop(false)

	require.False(t, f.started.IsBroken())

	caps := gst.NewCapsFromString("video/x-h264,stream-format=byte-stream,alignment=au")
	f.cache([]byte{0, 0, 0, 1, 0x65, 1, 2, 3}, caps)

	require.True(t, f.started.IsBroken(), "first cached keyframe should start the loop")
	require.True(t, f.capsSet)
	require.Equal(t, []byte{0, 0, 0, 1, 0x65, 1, 2, 3}, f.frame)

	// later keyframes replace the cached frame without resetting caps
	f.cache([]byte{0, 0, 0, 1, 0x65, 9}, nil)
	require.Equal(t, []byte{0, 0, 0, 1, 0x65, 9}, f.frame)
}

func TestFreezeInjector_EmptyOrMissingDataIgnored(t *testing.T) {
	initGStreamer(t)

	f := newTestInjector(t, func() bool { return false })
	defer f.Stop(false)

	f.cache(nil, nil)
	require.False(t, f.started.IsBroken())
	require.Nil(t, f.frame)
}

func TestFreezeInjector_InjectRequiresFrameAndCaps(t *testing.T) {
	initGStreamer(t)

	f := newTestInjector(t, func() bool { return true })
	defer f.Stop(false)

	// no frame cached: no-op
	f.injectFrame()
	require.Equal(t, uint64(0), f.framesInjected.Load())
	require.False(t, f.frozen.Load())

	// frame cached: injection proceeds (appsrc queues internally until playing)
	caps := gst.NewCapsFromString("video/x-h264,stream-format=byte-stream,alignment=au")
	f.cache([]byte{0, 0, 0, 1, 0x65, 1}, caps)
	f.injectFrame()
	require.True(t, f.frozen.Load(), "freeze mode entered on first injection attempt")
	require.Equal(t, uint64(1), f.framesInjected.Load())
}

func TestFreezeInjector_StallDetectorResetsFreezeState(t *testing.T) {
	initGStreamer(t)

	f := newTestInjector(t, func() bool { return false })
	defer f.Stop(false)

	f.frozen.Store(true)
	f.OnRealBuffer()
	f.checkStall()
	require.False(t, f.frozen.Load(), "resumed track should clear freeze state")
}
