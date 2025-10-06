// Copyright 2025 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sdk

import (
	"time"

	"github.com/livekit/media-sdk/jitter"
	"github.com/pion/webrtc/v4"
)

type burstEstimator struct {
	clockRate   uint32        // RTP clock rate for timestamp to duration conversion
	maxSkew     time.Duration // tolerated skew between RTP duration and arrival delta
	minArrival  time.Duration // lower bound on arrival delta to flag bursts
	scoreTarget int           // number of consecutive stable packets needed
	score       int           // running count of stable packets
	lastTS      uint32        // previous RTP timestamp
	lastArrival time.Time     // arrival time for the previous packet
	hasLast     bool          // indicates lastTS/lastArrival already recorded
	done        bool          // initialization deemed safe
	buffer      []jitter.ExtPacket
	maxBuffer   int
}

func newBurstEstimator(clockRate uint32, kind webrtc.RTPCodecType) *burstEstimator {
	be := &burstEstimator{
		clockRate:   clockRate,
		maxSkew:     20 * time.Millisecond,
		minArrival:  2 * time.Millisecond,
		scoreTarget: 5,
		maxBuffer:   1000, // key frames could take up to 600-700 pacejts for hight bitrate streams
	}

	if kind == webrtc.RTPCodecTypeAudio {
		be.maxSkew = 8 * time.Millisecond
		be.maxBuffer = 200
	}

	return be
}

// Push feeds one packet into the estimator.
// When pacing stabilizes, it returns the buffered packets that form the
// first good sequence, the number of packets dropped while waiting and a boolean
// indicating whether the estimator is done. Until stability is reached, it returns nil.
func (b *burstEstimator) Push(pkt jitter.ExtPacket) ([]jitter.ExtPacket, int, bool) {
	if b.done {
		return nil, 0, true
	}

	if len(pkt.Payload) == 0 {
		// ignore empty payloads, do not count as dropped
		return nil, 0, false
	}

	if !b.hasLast {
		b.lastTS = pkt.Timestamp
		b.lastArrival = pkt.ReceivedAt
		b.hasLast = true
		b.buffer = append(b.buffer[:0], pkt)
		return nil, 0, false
	}

	signedTsDelta := int32(pkt.Timestamp - b.lastTS)
	arrivalDelta := pkt.ReceivedAt.Sub(b.lastArrival)

	if signedTsDelta < 0 {
		// out of order packet
		return nil, 0, false
	}

	b.lastTS = pkt.Timestamp

	if signedTsDelta == 0 {
		// multiple packets with the same timestamp, e.g video key frame
		b.buffer = append(b.buffer, pkt)
		if dropped := b.enforceWindow(); dropped > 0 {
			return nil, dropped, false
		}
		return nil, 0, false
	}

	b.lastArrival = pkt.ReceivedAt

	tsDuration := b.timestampToDuration(uint32(signedTsDelta))
	if tsDuration <= 0 {
		dropped := len(b.buffer)
		b.buffer = b.buffer[:0]
		b.score = 0
		b.buffer = append(b.buffer, pkt)
		return nil, dropped, false
	}

	if arrivalDelta < b.minArrival {
		dropped := len(b.buffer)
		b.buffer = b.buffer[:0]
		b.score = 0
		b.buffer = append(b.buffer, pkt)
		return nil, dropped, false
	}

	skew := arrivalDelta - tsDuration
	if skew < 0 {
		skew = -skew
	}

	if skew > b.maxSkew {
		dropped := len(b.buffer)
		b.buffer = b.buffer[:0]
		b.score = 0
		b.buffer = append(b.buffer, pkt)
		return nil, dropped, false
	}

	b.buffer = append(b.buffer, pkt)
	if dropped := b.enforceWindow(); dropped > 0 {
		return nil, dropped, false
	}
	if b.score < b.scoreTarget {
		b.score++
	}
	if b.score >= b.scoreTarget {
		b.done = true
		ready := b.buffer
		b.buffer = nil
		return ready, 0, true
	}

	return nil, 0, false
}

func (b *burstEstimator) timestampToDuration(delta uint32) time.Duration {
	if b.clockRate == 0 {
		return 0
	}

	return time.Duration(int64(delta) * int64(time.Second) / int64(b.clockRate))
}

func (b *burstEstimator) enforceWindow() int {
	if b.maxBuffer == 0 || len(b.buffer) <= b.maxBuffer {
		return 0
	}
	dropped := len(b.buffer) - b.maxBuffer
	copy(b.buffer, b.buffer[dropped:])
	b.buffer = b.buffer[:len(b.buffer)-dropped]
	b.score = 0
	return dropped
}
