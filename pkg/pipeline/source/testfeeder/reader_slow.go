// Copyright 2024 LiveKit, Inc.
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

import "time"

// SlowReader wraps a FrameReader and adds a fixed delay before each frame read,
// simulating a source that produces data slower than the pipeline can consume.
// This is useful for testing that the non-live pipeline correctly handles
// asymmetric source rates via backpressure rather than dropping data.
type SlowReader struct {
	inner    FrameReader
	delay    time.Duration
}

// NewSlowReader wraps an existing FrameReader with a per-frame delay.
func NewSlowReader(inner FrameReader, delay time.Duration) *SlowReader {
	return &SlowReader{inner: inner, delay: delay}
}

func (r *SlowReader) NextFrame() ([]byte, uint32, error) {
	time.Sleep(r.delay)
	return r.inner.NextFrame()
}

func (r *SlowReader) ClockRate() uint32 {
	return r.inner.ClockRate()
}

func (r *SlowReader) Close() error {
	return r.inner.Close()
}
