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

import "time"

// opusGapFiller keeps Opus packets contiguous across DTX and loss so Ogg granules track real time
type opusGapFiller struct {
	started bool
	nextPTS time.Duration
	toc     byte
	frame   time.Duration
}

type opusFill struct {
	pts     time.Duration
	payload []byte
}

// fill returns one TOC-only packet per frame missing before a packet at pts
func (f *opusGapFiller) fill(pts time.Duration) []opusFill {
	if !f.started {
		return nil
	}
	n := int((pts - f.nextPTS + f.frame/2) / f.frame)
	if n < 1 {
		return nil
	}

	// a zero-length code-0 frame is decoded as packet loss concealment (RFC 6716 §3.2.1)
	payload := []byte{f.toc &^ 0x03}
	fills := make([]opusFill, n)
	for i := range fills {
		fills[i] = opusFill{pts: f.nextPTS + time.Duration(i)*f.frame, payload: payload}
	}
	return fills
}

// advance records a real packet at pts and returns its duration, or 0 if it can't be parsed
func (f *opusGapFiller) advance(pts time.Duration, pkt []byte) time.Duration {
	frame, count := opusFrameInfo(pkt)
	if count == 0 {
		return 0
	}

	f.started = true
	f.toc = pkt[0]
	f.frame = frame
	duration := frame * time.Duration(count)
	f.nextPTS = pts + duration
	return duration
}

// opusFrameInfo parses the TOC byte per RFC 6716 §3.1
func opusFrameInfo(pkt []byte) (time.Duration, int) {
	if len(pkt) == 0 {
		return 0, 0
	}

	config := pkt[0] >> 3
	var frame time.Duration
	switch {
	case config < 12:
		frame = [4]time.Duration{10, 20, 40, 60}[config%4] * time.Millisecond
	case config < 16:
		frame = [2]time.Duration{10, 20}[config%2] * time.Millisecond
	default:
		frame = [4]time.Duration{2500, 5000, 10000, 20000}[config%4] * time.Microsecond
	}

	switch pkt[0] & 0x03 {
	case 0:
		return frame, 1
	case 1, 2:
		return frame, 2
	default:
		if len(pkt) < 2 {
			return 0, 0
		}
		return frame, int(pkt[1] & 0x3f)
	}
}
