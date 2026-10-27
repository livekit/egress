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

const maxOpusPacketDuration = 120 * time.Millisecond

// opusGapFiller keeps Opus packets contiguous across DTX and loss so Ogg granules track real time
type opusGapFiller struct {
	started bool
	nextPTS time.Duration
	toc     byte
	frame   time.Duration
}

// fill pushes packets of empty frames covering every frame missing before a packet at pts
func (f *opusGapFiller) fill(pts time.Duration, push func(data []byte, pts, duration time.Duration) error) error {
	if !f.started {
		return nil
	}

	missing := int((pts - f.nextPTS) / f.frame)
	perPacket := int(maxOpusPacketDuration / f.frame)
	at := f.nextPTS
	for missing > 0 {
		m := min(missing, perPacket)
		// a code-3 CBR packet of zero-length frames is decoded as packet loss concealment (RFC 6716 §3.2.1, §3.2.5)
		payload := []byte{f.toc | 0x03, byte(m)}
		duration := time.Duration(m) * f.frame
		if err := push(payload, at, duration); err != nil {
			return err
		}
		at += duration
		missing -= m
	}
	return nil
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
