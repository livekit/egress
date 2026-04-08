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

import (
	"fmt"
	"io"
	"time"
)

// ErrGap is returned by a FrameReader to signal a gap in the media data.
// The feeder should send a GStreamer GAP event for the specified duration
// instead of pushing a buffer, then continue reading.
type ErrGap struct {
	Duration time.Duration
}

func (e *ErrGap) Error() string {
	return fmt.Sprintf("gap: %v", e.Duration)
}

// GappingReader wraps a FrameReader and introduces a gap after a fixed number
// of frames. After delivering gapAfter frames, it returns an ErrGap with the
// specified duration. After the gap, it continues reading from the inner reader
// until EOF.
type GappingReader struct {
	inner       FrameReader
	gapAfter    int
	gapDuration time.Duration
	delivered   int
	gapSent     bool
}

// NewGappingReader wraps an existing FrameReader. After gapAfter frames, the
// next call to NextFrame returns ErrGap with the given duration. Subsequent
// calls continue reading from inner.
func NewGappingReader(inner FrameReader, gapAfter int, gapDuration time.Duration) *GappingReader {
	return &GappingReader{
		inner:       inner,
		gapAfter:    gapAfter,
		gapDuration: gapDuration,
	}
}

func (r *GappingReader) NextFrame() ([]byte, uint32, error) {
	if r.delivered >= r.gapAfter && !r.gapSent {
		r.gapSent = true
		return nil, 0, &ErrGap{Duration: r.gapDuration}
	}

	payload, samples, err := r.inner.NextFrame()
	if err != nil {
		if err == io.EOF {
			return nil, 0, io.EOF
		}
		return nil, 0, err
	}
	r.delivered++
	return payload, samples, nil
}

func (r *GappingReader) ClockRate() uint32 {
	return r.inner.ClockRate()
}

func (r *GappingReader) Close() error {
	return r.inner.Close()
}
