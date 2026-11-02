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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

const testPLIPeriod = 10 * time.Millisecond

func newTestRequester() (*keyframeRequester, *atomic.Int32) {
	var count atomic.Int32
	return newKeyframeRequester(testPLIPeriod, func() { count.Inc() }), &count
}

func TestKeyframeRequester_RequestPLIIsOneShot(t *testing.T) {
	r, count := newTestRequester()
	defer r.Close()

	for i := 0; i < 5; i++ {
		r.RequestPLI()
	}
	// one immediate PLI, plus at most one trailing PLI for the burst
	require.Eventually(t, func() bool { return count.Load() == 2 }, time.Second, time.Millisecond)
	time.Sleep(10 * testPLIPeriod)
	require.Equal(t, int32(2), count.Load())
}

func TestKeyframeRequester_RepeatsUntilKeyframe(t *testing.T) {
	r, count := newTestRequester()
	defer r.Close()

	r.RequirePLI()
	require.Equal(t, int32(1), count.Load(), "first PLI should be sent immediately")
	require.Eventually(t, func() bool { return count.Load() >= 4 }, time.Second, time.Millisecond)

	r.KeyframeReceived()
	settled := count.Load()
	time.Sleep(10 * testPLIPeriod)
	require.LessOrEqual(t, count.Load(), settled+1, "at most one queued PLI can follow the keyframe")
}

func TestKeyframeRequester_CloseStopsRequests(t *testing.T) {
	r, count := newTestRequester()

	r.RequirePLI()
	require.Eventually(t, func() bool { return count.Load() >= 2 }, time.Second, time.Millisecond)

	r.Close()
	closed := count.Load()
	time.Sleep(10 * testPLIPeriod)
	require.Equal(t, closed, count.Load())

	r.RequirePLI()
	r.RequestPLI()
	time.Sleep(5 * testPLIPeriod)
	require.Equal(t, closed, count.Load())
}
