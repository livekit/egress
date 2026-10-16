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
	"time"

	"github.com/pion/rtp"

	"github.com/livekit/egress/pkg/types"
)

const (
	// audio per synthetic packet
	maxConcealmentPacket = 120 * time.Millisecond

	// an RTP gap this far beyond elapsed real time is a timestamp jump, not a pause
	maxGapLead = 500 * time.Millisecond
)

// gapCodec measures a codec's packets and builds concealment for them
type gapCodec struct {
	// samples per frame and frame count of a payload; count 0 if unparseable
	frames func(payload []byte, clockRate uint32) (samples uint32, count int)
	// payload decoding to count frames of frame duration of concealment, given the last
	// real payload; nil if the codec has no such frame
	conceal func(last []byte, count int, frame time.Duration) []byte
}

var (
	opusSilkFrames   = [4]time.Duration{10, 20, 40, 60}
	opusHybridFrames = [2]time.Duration{10, 20}
	opusCeltFrames   = [4]time.Duration{2500, 5000, 10000, 20000}
)

// opusGapCodec conceals with a code-3 packet of zero-length frames, decoded as packet
// loss (RFC 6716 §3.2.1, §3.2.5)
var opusGapCodec = gapCodec{
	frames: func(payload []byte, clockRate uint32) (uint32, int) {
		frame, count := opusFrameInfo(payload)
		return uint32(frame * time.Duration(clockRate) / time.Second), count
	},
	conceal: func(last []byte, count int, frame time.Duration) []byte {
		toc, ok := opusConcealTOC(last[0], frame)
		if !ok {
			return nil
		}
		return []byte{toc, byte(count)}
	},
}

// opusConcealTOC returns a code-3 TOC for frames of the given duration in last's bandwidth
// and channel layout, falling back to CELT fullband, which has every size under 20ms
func opusConcealTOC(last byte, frame time.Duration) (byte, bool) {
	config := last >> 3
	var base byte
	var sizes []time.Duration
	switch {
	case config < 12:
		base, sizes = config&^3, opusSilkFrames[:]
		frame /= time.Millisecond
	case config < 16:
		base, sizes = 12+(config-12)&^1, opusHybridFrames[:]
		frame /= time.Millisecond
	default:
		base, sizes = 16+(config-16)&^3, opusCeltFrames[:]
		frame /= time.Microsecond
	}
	for i, size := range sizes {
		if size == frame {
			return (base+byte(i))<<3 | last&0x04 | 0x03, true
		}
	}
	if config < 16 {
		frame *= time.Millisecond / time.Microsecond
	}
	for i, size := range opusCeltFrames {
		if size == frame {
			return (28+byte(i))<<3 | last&0x04 | 0x03, true
		}
	}
	return 0, false
}

// g711GapCodec conceals with silence bytes; a packet is one frame, one byte per sample
func g711GapCodec(silence byte) gapCodec {
	return gapCodec{
		frames: func(payload []byte, _ uint32) (uint32, int) {
			if len(payload) == 0 {
				return 0, 0
			}
			return uint32(len(payload)), 1
		},
		conceal: func(_ []byte, count int, frame time.Duration) []byte {
			b := make([]byte, count*int(frame*8000/time.Second))
			for i := range b {
				b[i] = silence
			}
			return b
		},
	}
}

// RTPGapFiller keeps a track contiguous across DTX pauses and loss by inserting
// concealment packets, so the pacer's resampling is the only change to the sample count.
// Gaps are measured on RTP timestamps: PTS differ from the sample count by the clock
// skew the pacer absorbs.
type RTPGapFiller struct {
	clockRate uint32
	codec     gapCodec

	started   bool
	seqOffset uint16     // added to real packets, past the synthetic ones
	next      rtp.Header // expected sequence number and timestamp of the next packet
	frame     uint32     // samples per frame of the last real packet
	last      []byte     // payload of the last real packet
	lastTS    uint32
	lastPTS   time.Duration
}

// NewRTPGapFiller returns a filler for an Opus or G.711 track, or nil for other codecs
func NewRTPGapFiller(mimeType types.MimeType, clockRate uint32) *RTPGapFiller {
	switch mimeType {
	case types.MimeTypeOpus:
		return newRTPGapFiller(clockRate, opusGapCodec)
	case types.MimeTypePCMU:
		return newRTPGapFiller(clockRate, g711GapCodec(0xff))
	case types.MimeTypePCMA:
		return newRTPGapFiller(clockRate, g711GapCodec(0xd5))
	default:
		return nil
	}
}

func newRTPGapFiller(clockRate uint32, codec gapCodec) *RTPGapFiller {
	return &RTPGapFiller{clockRate: clockRate, codec: codec}
}

// Fill pushes concealment for the frames missing before pkt, a shorter frame for any
// remainder the codec can express, and shifts pkt's sequence number past earlier
// synthetic packets. Lost packets' numbers are reused first. pts is pkt's PTS; an RTP gap
// far beyond the real time since the last accepted packet is a timestamp jump and starts
// a new run.
func (f *RTPGapFiller) Fill(pkt *rtp.Packet, pts time.Duration, push func(*rtp.Packet, time.Duration) error) error {
	pkt.SequenceNumber += f.seqOffset
	if !f.started {
		return nil
	}

	gap := int32(pkt.Timestamp - f.next.Timestamp)
	if gap <= 0 {
		return nil
	}
	if f.duration(uint32(gap)) > pts-f.lastPTS+maxGapLead {
		f.Reset()
		return nil
	}
	lost := int(int16(pkt.SequenceNumber - f.next.SequenceNumber))

	emit := func(count int, samples uint32) error {
		payload := f.codec.conceal(f.last, count, f.duration(samples))
		if payload == nil {
			return nil
		}
		synth := &rtp.Packet{Header: f.next, Payload: payload}
		if err := push(synth, f.lastPTS+f.duration(synth.Timestamp-f.lastTS)); err != nil {
			return err
		}
		f.next.SequenceNumber++
		f.next.Timestamp += uint32(count) * samples
		if lost > 0 {
			lost--
		} else {
			f.seqOffset++
			pkt.SequenceNumber++
		}
		return nil
	}

	missing := int(uint32(gap) / f.frame)
	perPacket := max(1, min(48, int(uint32(maxConcealmentPacket*time.Duration(f.clockRate)/time.Second)/f.frame)))
	for missing > 0 {
		count := min(missing, perPacket)
		if err := emit(count, f.frame); err != nil {
			return err
		}
		missing -= count
	}
	if remainder := uint32(gap) % f.frame; remainder > 0 {
		return emit(1, remainder)
	}
	return nil
}

// Advance records a real packet once it has been pushed with pts
func (f *RTPGapFiller) Advance(pkt *rtp.Packet, pts time.Duration) {
	samples, count := f.codec.frames(pkt.Payload, f.clockRate)
	if count == 0 || samples == 0 {
		f.started = false
		return
	}
	f.started = true
	f.frame = samples
	f.last = pkt.Payload
	f.lastTS = pkt.Timestamp
	f.lastPTS = pts
	f.next = rtp.Header{
		Version:        2,
		PayloadType:    pkt.PayloadType,
		SSRC:           pkt.SSRC,
		SequenceNumber: pkt.SequenceNumber + 1,
		Timestamp:      pkt.Timestamp + samples*uint32(count),
	}
}

// Reset starts a new run at the next packet; the sequence shift is kept
func (f *RTPGapFiller) Reset() {
	f.started = false
}

// Skip moves the expected position past d of audio that reached the pipeline without
// packets, such as a GAP event, so the next Fill conceals only what remains of the gap
func (f *RTPGapFiller) Skip(d time.Duration) {
	if !f.started || d <= 0 {
		return
	}
	f.next.Timestamp += uint32(d * time.Duration(f.clockRate) / time.Second)
}

func (f *RTPGapFiller) duration(samples uint32) time.Duration {
	return time.Duration(samples) * time.Second / time.Duration(f.clockRate)
}
