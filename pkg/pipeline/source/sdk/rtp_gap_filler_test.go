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

	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

const (
	toc20ms = 0x08 // config 1 (SILK, 20ms), mono, code 0
	toc60ms = 0x18 // config 3 (SILK, 60ms), mono, code 0
)

func opusPacket(seq uint16, ts uint32, toc byte) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 111, SSRC: 7, SequenceNumber: seq, Timestamp: ts},
		Payload: []byte{toc, 0xaa, 0xbb},
	}
}

type pushedPacket struct {
	pkt *rtp.Packet
	pts time.Duration
}

func collectPushes(into *[]pushedPacket) func(*rtp.Packet, time.Duration) error {
	return func(p *rtp.Packet, pts time.Duration) error {
		*into = append(*into, pushedPacket{p, pts})
		return nil
	}
}

// fillPaused fills before pkt as if real time had passed in step with its RTP timestamp
func fillPaused(f *rtpGapFiller, pkt *rtp.Packet, push func(*rtp.Packet, time.Duration) error) error {
	pts := f.lastPTS + time.Duration(int32(pkt.Timestamp-f.lastTS))*time.Second/time.Duration(f.clockRate)
	return f.fill(pkt, pts, push)
}

func TestRTPGapFillerContiguousPacketsPassUntouched(t *testing.T) {
	f := newRTPGapFiller(48000, opusGapCodec)
	var pushes []pushedPacket

	p1 := opusPacket(10, 0, toc20ms)
	require.NoError(t, fillPaused(f, p1, collectPushes(&pushes)))
	f.advance(p1, time.Second)

	p2 := opusPacket(11, 960, toc20ms)
	require.NoError(t, fillPaused(f, p2, collectPushes(&pushes)))
	require.Empty(t, pushes)
	require.Equal(t, uint16(11), p2.SequenceNumber)
}

func TestRTPGapFillerFillsMissingFramesBeforeThePacket(t *testing.T) {
	f := newRTPGapFiller(48000, opusGapCodec)
	var pushes []pushedPacket

	p1 := opusPacket(10, 0, toc20ms)
	require.NoError(t, fillPaused(f, p1, collectPushes(&pushes)))
	f.advance(p1, time.Second)

	// 100ms of silence between p1 and p2
	p2 := opusPacket(11, 960*6, toc20ms)
	require.NoError(t, fillPaused(f, p2, collectPushes(&pushes)))

	require.Len(t, pushes, 1)
	synth := pushes[0].pkt
	require.Equal(t, uint16(11), synth.SequenceNumber)
	require.Equal(t, uint32(960), synth.Timestamp)
	require.Equal(t, uint8(111), synth.PayloadType)
	require.Equal(t, uint32(7), synth.SSRC)
	require.Equal(t, []byte{toc20ms | 0x03, 5}, synth.Payload, "a code-3 packet of 5 zero-length frames")
	require.Equal(t, time.Second+20*time.Millisecond, pushes[0].pts, "PTS extrapolated from the last real packet")
	require.Equal(t, uint16(12), p2.SequenceNumber, "the real packet is shifted past the synthetic one")

	f.advance(p2, time.Second+120*time.Millisecond)
	p3 := opusPacket(12, 960*7, toc20ms)
	require.NoError(t, fillPaused(f, p3, collectPushes(&pushes)))
	require.Len(t, pushes, 1)
	require.Equal(t, uint16(13), p3.SequenceNumber, "the shift persists")
}

func TestRTPGapFillerChunksLongGaps(t *testing.T) {
	f := newRTPGapFiller(48000, opusGapCodec)
	var pushes []pushedPacket

	p1 := opusPacket(10, 0, toc20ms)
	require.NoError(t, fillPaused(f, p1, collectPushes(&pushes)))
	f.advance(p1, 0)

	// 2s of silence: 100 frames, at most 120ms per synthetic packet
	p2 := opusPacket(11, 960+100*960, toc20ms)
	require.NoError(t, fillPaused(f, p2, collectPushes(&pushes)))

	require.Len(t, pushes, 17)
	var frames int
	for i, p := range pushes {
		require.Equal(t, uint16(11+i), p.pkt.SequenceNumber)
		require.Equal(t, uint32(960+frames*960), p.pkt.Timestamp)
		require.Equal(t, time.Duration(1+frames)*20*time.Millisecond, p.pts)
		frames += int(p.pkt.Payload[1])
	}
	require.Equal(t, 100, frames)
	require.Equal(t, uint16(28), p2.SequenceNumber)
}

func TestRTPGapFillerConcealsSubFrameRemainderWithAShorterFrame(t *testing.T) {
	f := newRTPGapFiller(48000, opusGapCodec)
	var pushes []pushedPacket

	p1 := opusPacket(10, 0, toc20ms)
	require.NoError(t, fillPaused(f, p1, collectPushes(&pushes)))
	f.advance(p1, 0)

	// a 30ms gap: one 20ms frame, then a 10ms frame of the same SILK band
	p2 := opusPacket(11, 960+1440, toc20ms)
	require.NoError(t, fillPaused(f, p2, collectPushes(&pushes)))
	require.Len(t, pushes, 2)
	require.Equal(t, []byte{toc20ms | 0x03, 1}, pushes[0].pkt.Payload)
	require.Equal(t, []byte{0x03, 1}, pushes[1].pkt.Payload, "config 0 is SILK NB 10ms")
	require.Equal(t, uint32(960+960), pushes[1].pkt.Timestamp)
	require.Equal(t, uint16(13), p2.SequenceNumber)
	f.advance(p2, 50*time.Millisecond)

	// two successive 10ms gaps each get their own 10ms frame
	prev := p2
	for i, seq := range []uint16{12, 13} {
		p := opusPacket(seq, prev.Timestamp+960+480, toc20ms)
		require.NoError(t, fillPaused(f, p, collectPushes(&pushes)))
		require.Len(t, pushes, 3+i)
		require.Equal(t, []byte{0x03, 1}, pushes[2+i].pkt.Payload)
		f.advance(p, 0)
		prev = p
	}
}

func TestRTPGapFillerFillsARunOfRejectedPackets(t *testing.T) {
	f := newRTPGapFiller(48000, opusGapCodec)
	var pushes []pushedPacket

	p1 := opusPacket(10, 0, toc20ms)
	require.NoError(t, fillPaused(f, p1, collectPushes(&pushes)))
	f.advance(p1, time.Second)

	// 30 packets were rejected by appsrc: real time moved 620ms, the filler did not
	p := opusPacket(41, 960*31, toc20ms)
	require.NoError(t, f.fill(p, time.Second+620*time.Millisecond, collectPushes(&pushes)))
	var frames int
	for _, push := range pushes {
		frames += int(push.pkt.Payload[1])
	}
	require.Equal(t, 30, frames)
	require.Equal(t, uint16(41), p.SequenceNumber, "the rejected packets' numbers are reused")
}

func TestOpusConcealTOC(t *testing.T) {
	for _, tc := range []struct {
		name  string
		last  byte
		frame time.Duration
		toc   byte
		ok    bool
	}{
		{"silk wb 20ms keeps its config", 0x48, 20 * time.Millisecond, 0x4b, true},
		{"silk wb 10ms", 0x48, 10 * time.Millisecond, 0x43, true},
		{"silk wb stereo 60ms", 0x4c, 60 * time.Millisecond, 0x5f, true},
		{"silk has no 5ms, celt fullband does", 0x48, 5 * time.Millisecond, 0xeb, true},
		{"hybrid fb 10ms", 0x78, 10 * time.Millisecond, 0x73, true},
		{"celt fb 2.5ms", 0xf8, 2500 * time.Microsecond, 0xe3, true},
		{"no 15ms frame", 0xf8, 15 * time.Millisecond, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			toc, ok := opusConcealTOC(tc.last, tc.frame)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.toc, toc)
		})
	}
}

func TestRTPGapFillerReusesLostPacketsSequenceNumbers(t *testing.T) {
	f := newRTPGapFiller(48000, opusGapCodec)
	var pushes []pushedPacket

	p1 := opusPacket(10, 0, toc20ms)
	require.NoError(t, fillPaused(f, p1, collectPushes(&pushes)))
	f.advance(p1, 0)

	// packet 11 is lost: its concealment takes its number and packet 12 keeps its own
	p3 := opusPacket(12, 960*2, toc20ms)
	require.NoError(t, fillPaused(f, p3, collectPushes(&pushes)))
	require.Len(t, pushes, 1)
	require.Equal(t, uint16(11), pushes[0].pkt.SequenceNumber)
	require.Equal(t, uint16(12), p3.SequenceNumber)
	require.Equal(t, uint16(0), f.seqOffset)
	f.advance(p3, 40*time.Millisecond)

	// packets 13 and 14 are lost across a 2s DTX pause: 17 synthetic packets, two take the
	// lost numbers and the rest shift the real packet
	p5 := opusPacket(15, p3.Timestamp+960+100*960, toc20ms)
	require.NoError(t, fillPaused(f, p5, collectPushes(&pushes)))
	require.Len(t, pushes, 18)
	require.Equal(t, uint16(13), pushes[1].pkt.SequenceNumber)
	require.Equal(t, uint16(29), pushes[17].pkt.SequenceNumber)
	require.Equal(t, uint16(30), p5.SequenceNumber)
	require.Equal(t, uint16(15), f.seqOffset)
}

func TestRTPGapFillerFollowsTheLastPacketsFrameSize(t *testing.T) {
	f := newRTPGapFiller(48000, opusGapCodec)
	var pushes []pushedPacket

	p1 := opusPacket(10, 0, toc60ms)
	require.NoError(t, fillPaused(f, p1, collectPushes(&pushes)))
	f.advance(p1, 0)
	require.Equal(t, uint32(2880), f.frame)

	p2 := opusPacket(11, 2880, toc20ms)
	require.NoError(t, fillPaused(f, p2, collectPushes(&pushes)))
	require.Empty(t, pushes)
	f.advance(p2, 60*time.Millisecond)
	require.Equal(t, uint32(960), f.frame)

	p3 := opusPacket(12, 2880+960+960, toc20ms)
	require.NoError(t, fillPaused(f, p3, collectPushes(&pushes)))
	require.Len(t, pushes, 1)
	require.Equal(t, []byte{toc20ms | 0x03, 1}, pushes[0].pkt.Payload)
}

func TestRTPGapFillerIgnoresReorderedPackets(t *testing.T) {
	f := newRTPGapFiller(48000, opusGapCodec)
	var pushes []pushedPacket

	p1 := opusPacket(10, 960, toc20ms)
	require.NoError(t, fillPaused(f, p1, collectPushes(&pushes)))
	f.advance(p1, 20*time.Millisecond)

	late := opusPacket(9, 0, toc20ms)
	require.NoError(t, fillPaused(f, late, collectPushes(&pushes)))
	require.Empty(t, pushes)
}

func TestRTPGapFillerResetKeepsTheSequenceShift(t *testing.T) {
	f := newRTPGapFiller(48000, opusGapCodec)
	var pushes []pushedPacket

	p1 := opusPacket(10, 0, toc20ms)
	require.NoError(t, fillPaused(f, p1, collectPushes(&pushes)))
	f.advance(p1, 0)
	p2 := opusPacket(11, 960*3, toc20ms)
	require.NoError(t, fillPaused(f, p2, collectPushes(&pushes)))
	require.Len(t, pushes, 1)
	f.advance(p2, 60*time.Millisecond)

	f.reset()
	// a packet far ahead after a reset is not filled, but still keeps its number past the synthetic one
	p3 := opusPacket(12, 960*500, toc20ms)
	require.NoError(t, fillPaused(f, p3, collectPushes(&pushes)))
	require.Len(t, pushes, 1)
	require.Equal(t, uint16(13), p3.SequenceNumber)

	f.advance(p3, 10*time.Second)
	p4 := opusPacket(13, 960*502, toc20ms)
	require.NoError(t, fillPaused(f, p4, collectPushes(&pushes)))
	require.Len(t, pushes, 2, "filling resumes from the first packet after the reset")
}

func TestRTPGapFillerIgnoresTimestampJumpsWithoutAPause(t *testing.T) {
	f := newRTPGapFiller(48000, opusGapCodec)
	var pushes []pushedPacket

	p1 := opusPacket(10, 0, toc20ms)
	require.NoError(t, fillPaused(f, p1, collectPushes(&pushes)))
	f.advance(p1, time.Second)

	// RTP jumps 10s while only one frame of real time passed: a restart, not a pause
	p2 := opusPacket(11, 960+48000*10, toc20ms)
	require.NoError(t, f.fill(p2, 20*time.Millisecond, collectPushes(&pushes)))
	require.Empty(t, pushes)
	require.Equal(t, uint16(11), p2.SequenceNumber)
	f.advance(p2, time.Second+20*time.Millisecond)

	// filling resumes on the new run
	p3 := opusPacket(12, p2.Timestamp+960*3, toc20ms)
	require.NoError(t, fillPaused(f, p3, collectPushes(&pushes)))
	require.Len(t, pushes, 1)
	require.Equal(t, uint8(2), pushes[0].pkt.Payload[1])
}

func TestRTPGapFillerG711Silence(t *testing.T) {
	f := newRTPGapFiller(8000, g711GapCodec(0xff))
	var pushes []pushedPacket

	payload := make([]byte, 160)
	p1 := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 1, Timestamp: 0}, Payload: payload}
	require.NoError(t, fillPaused(f, p1, collectPushes(&pushes)))
	f.advance(p1, 0)
	require.Equal(t, uint32(160), f.frame)

	// 100ms gap: five 20ms frames, within the 120ms packet bound
	p2 := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 2, Timestamp: 160 + 800}, Payload: payload}
	require.NoError(t, fillPaused(f, p2, collectPushes(&pushes)))
	require.Len(t, pushes, 1)
	require.Len(t, pushes[0].pkt.Payload, 800)
	for _, b := range pushes[0].pkt.Payload {
		require.Equal(t, byte(0xff), b)
	}
	require.Equal(t, 20*time.Millisecond, pushes[0].pts, "the fill starts one frame after the last real packet")
}
