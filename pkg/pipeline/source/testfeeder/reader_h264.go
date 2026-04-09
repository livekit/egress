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

	"github.com/pion/webrtc/v4/pkg/media/h264reader"
)

// H264Reader reads H.264 NAL units from an Annex B bitstream file and presents
// them as frames suitable for RTP packetization.
//
// Each call to NextFrame returns a complete NAL unit. The frame duration is
// fixed at (clockRate / fps) samples, which is correct for constant-framerate
// content. For VFR content the duration is approximate, but sufficient for
// pipeline testing.
type H264Reader struct {
	reader *h264reader.H264Reader
	file   *os.File
	fps    int
}

// NewH264Reader opens an H.264 Annex B file and returns a FrameReader.
// fps sets the assumed frame rate for timestamp advancement.
func NewH264Reader(path string, fps int) (*H264Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	h, err := h264reader.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}

	if fps <= 0 {
		fps = defaultVideoFPS
	}

	return &H264Reader{
		reader: h,
		file:   f,
		fps:    fps,
	}, nil
}

func (r *H264Reader) NextFrame() ([]byte, uint32, error) {
	nal, err := r.reader.NextNAL()
	if err != nil {
		if err == io.EOF {
			return nil, 0, io.EOF
		}
		return nil, 0, err
	}

	samples := uint32(videoClockRate / r.fps)
	return nal.Data, samples, nil
}

func (r *H264Reader) ClockRate() uint32 {
	return videoClockRate
}

func (r *H264Reader) Close() error {
	return r.file.Close()
}
