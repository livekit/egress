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

package pipeline

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStallDetector(t *testing.T) {
	start := time.Unix(0, 0)
	at := func(s int) time.Time { return start.Add(time.Duration(s) * time.Second) }
	pos := func(s int) time.Duration { return time.Duration(s) * time.Second }

	t.Run("reports a stall once and its end once", func(t *testing.T) {
		d := newStallDetector(start, 10*time.Second)
		for s := 1; s <= 5; s++ {
			event, _ := d.observe(at(s), pos(s*3), true)
			require.Equal(t, stallNone, event)
		}

		// the position stays at 15 s from second 5 on
		for s := 6; s < 15; s++ {
			event, _ := d.observe(at(s), pos(15), true)
			require.Equal(t, stallNone, event)
		}
		event, stalledFor := d.observe(at(15), pos(15), true)
		require.Equal(t, stallStarted, event)
		require.Equal(t, 10*time.Second, stalledFor)
		require.Equal(t, pos(15), d.position)
		for s := 16; s < 40; s++ {
			event, _ = d.observe(at(s), pos(15), true)
			require.Equal(t, stallNone, event)
		}

		event, stalledFor = d.observe(at(40), pos(16), true)
		require.Equal(t, stallEnded, event)
		require.Equal(t, 35*time.Second, stalledFor)
		event, _ = d.observe(at(41), pos(17), true)
		require.Equal(t, stallNone, event)
	})

	t.Run("an output that never starts is stalled", func(t *testing.T) {
		d := newStallDetector(start, 10*time.Second)
		for s := 1; s < 10; s++ {
			event, _ := d.observe(at(s), 0, true)
			require.Equal(t, stallNone, event)
		}
		event, _ := d.observe(at(10), 0, true)
		require.Equal(t, stallStarted, event)
	})

	t.Run("failed and backward readings are not progress", func(t *testing.T) {
		d := newStallDetector(start, 10*time.Second)
		event, _ := d.observe(at(1), pos(20), true)
		require.Equal(t, stallNone, event)
		for s := 2; s < 11; s++ {
			ok := s%2 == 0
			event, _ = d.observe(at(s), pos(5), ok)
			require.Equal(t, stallNone, event)
		}
		event, stalledFor := d.observe(at(11), 0, false)
		require.Equal(t, stallStarted, event)
		require.Equal(t, 10*time.Second, stalledFor)
	})
}
