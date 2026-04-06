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

import (
	"io"
	"os"

	"github.com/pion/webrtc/v4/pkg/media/ivfreader"
)

// IVFReader reads VP8 or VP9 frames from an IVF container file.
// The IVF container provides per-frame timestamps, so duration is computed from
// the difference between consecutive frame timestamps (converted to clock rate
// units via the IVF timebase).
type IVFReader struct {
	reader    *ivfreader.IVFReader
	file      *os.File
	header    *ivfreader.IVFFileHeader
	lastPTS   uint64
	firstRead bool
}

// NewIVFReader opens an IVF file and returns a FrameReader.
func NewIVFReader(path string) (*IVFReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	reader, header, err := ivfreader.NewWith(f)
	if err != nil {
		f.Close()
		return nil, err
	}

	return &IVFReader{
		reader:    reader,
		file:      f,
		header:    header,
		firstRead: true,
	}, nil
}

func (r *IVFReader) NextFrame() ([]byte, uint32, error) {
	payload, frameHeader, err := r.reader.ParseNextFrame()
	if err != nil {
		if err == io.EOF {
			return nil, 0, io.EOF
		}
		return nil, 0, err
	}

	var samples uint32
	if r.firstRead {
		// First frame: use default frame duration based on the timebase.
		// timebaseDenominator is fps (e.g., 24), timebaseNumerator is typically 1.
		samples = videoClockRate * uint32(r.header.TimebaseNumerator) / uint32(r.header.TimebaseDenominator)
		r.firstRead = false
	} else {
		// Duration = difference between this frame's timestamp and the last.
		// IVF timestamps are in timebase units (numerator/denominator seconds).
		diff := frameHeader.Timestamp - r.lastPTS
		samples = uint32(diff * uint64(videoClockRate) * uint64(r.header.TimebaseNumerator) / uint64(r.header.TimebaseDenominator))
		if samples == 0 {
			// Fallback for zero-duration frames.
			samples = videoClockRate * uint32(r.header.TimebaseNumerator) / uint32(r.header.TimebaseDenominator)
		}
	}
	r.lastPTS = frameHeader.Timestamp

	return payload, samples, nil
}

func (r *IVFReader) ClockRate() uint32 {
	return videoClockRate
}

func (r *IVFReader) Close() error {
	return r.file.Close()
}
