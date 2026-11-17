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
)

func TestOpusGapFiller(t *testing.T) {
	// CELT fullband, stereo, code 1: two 20ms frames per packet
	pkt := []byte{0xfd, 0x01, 0x02}
	const packet = 40 * time.Millisecond

	var pts []time.Duration
	var at time.Duration
	add := func(n int, step time.Duration) {
		for range n {
			pts = append(pts, at)
			at += step
		}
	}
	add(5, packet)               // speech
	add(3, 400*time.Millisecond) // DTX
	add(5, packet)               // speech
	at += 80 * time.Millisecond  // four lost 20ms frames
	add(5, packet)

	var f opusGapFiller
	var end time.Duration
	var fillPackets, fillFrames int
	push := func(data []byte, pts, duration time.Duration) error {
		require.Equal(t, byte(0xff), data[0])
		require.Equal(t, end, pts)
		require.Equal(t, time.Duration(data[1])*f.frame, duration)
		end += duration
		fillPackets++
		fillFrames += int(data[1])
		return nil
	}
	for i, p := range pts {
		require.NoError(t, f.fill(p, push))
		if i > 0 {
			require.Equal(t, end, p, "packet %d is not contiguous with what was emitted before it", i)
		}
		end = p + f.advance(p, pkt)
	}
	require.Equal(t, 3*18+4, fillFrames)
	require.Equal(t, 3*3+1, fillPackets)
}
