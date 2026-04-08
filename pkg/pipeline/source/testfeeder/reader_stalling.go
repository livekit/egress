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

package testfeeder

import "io"

// StallingReader wraps a FrameReader and blocks forever after delivering a
// fixed number of frames. This simulates a track that has a gap in its
// recording — data stops arriving but no EOS or GAP event is sent.
type StallingReader struct {
	inner     FrameReader
	maxFrames int
	delivered int
	stallCh   chan struct{} // closed when the reader starts stalling
	unblockCh chan struct{} // close this to unblock the stall (e.g. for cleanup)
}

// NewStallingReader wraps an existing FrameReader. After maxFrames have been
// read, NextFrame blocks until unblockCh is closed (or forever if nil).
// stallCh is closed when the stall begins, allowing the test to detect it.
func NewStallingReader(inner FrameReader, maxFrames int) *StallingReader {
	return &StallingReader{
		inner:     inner,
		maxFrames: maxFrames,
		stallCh:   make(chan struct{}),
		unblockCh: make(chan struct{}),
	}
}

// Stalled returns a channel that is closed when the reader begins stalling.
func (r *StallingReader) Stalled() <-chan struct{} {
	return r.stallCh
}

// Unblock releases the stalled reader so the test can clean up. After
// unblocking, NextFrame returns io.EOF.
func (r *StallingReader) Unblock() {
	select {
	case <-r.unblockCh:
	default:
		close(r.unblockCh)
	}
}

func (r *StallingReader) NextFrame() ([]byte, uint32, error) {
	if r.delivered >= r.maxFrames {
		// Signal that we've started stalling.
		select {
		case <-r.stallCh:
		default:
			close(r.stallCh)
		}
		// Block until unblocked (test cleanup).
		<-r.unblockCh
		return nil, 0, io.EOF
	}

	payload, samples, err := r.inner.NextFrame()
	if err != nil {
		return nil, 0, err
	}
	r.delivered++
	return payload, samples, nil
}

func (r *StallingReader) ClockRate() uint32 {
	return r.inner.ClockRate()
}

func (r *StallingReader) Close() error {
	r.Unblock()
	return r.inner.Close()
}
