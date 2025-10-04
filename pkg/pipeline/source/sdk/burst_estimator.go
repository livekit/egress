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

	"github.com/pion/rtp"
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
}

func newBurstEstimator(clockRate uint32, kind webrtc.RTPCodecType) *burstEstimator {
	be := &burstEstimator{
		clockRate:   clockRate,
		maxSkew:     20 * time.Millisecond,
		minArrival:  2 * time.Millisecond,
		scoreTarget: 5,
	}

	if kind == webrtc.RTPCodecTypeAudio {
		be.maxSkew = 8 * time.Millisecond
	}

	return be
}

func (b *burstEstimator) ShouldInitialize(pkt *rtp.Packet, receivedAt time.Time) bool {
	if b.done {
		return true
	}

	if !b.hasLast {
		b.lastTS = pkt.Timestamp
		b.lastArrival = receivedAt
		b.hasLast = true
		return false
	}

	tsDelta := pkt.Timestamp - b.lastTS
	arrivalDelta := receivedAt.Sub(b.lastArrival)

	b.lastTS = pkt.Timestamp
	b.lastArrival = receivedAt

	if tsDelta == 0 {
		return false
	}

	tsDuration := b.timestampToDuration(tsDelta)
	if tsDuration <= 0 {
		return false
	}

	if arrivalDelta < b.minArrival {
		b.score = 0
		return false
	}

	skew := arrivalDelta - tsDuration
	if skew < 0 {
		skew = -skew
	}

	if skew > b.maxSkew {
		b.score = 0
		return false
	}

	if b.score < b.scoreTarget {
		b.score++
	}
	if b.score >= b.scoreTarget {
		b.done = true
		return true
	}

	return false
}

func (b *burstEstimator) timestampToDuration(delta uint32) time.Duration {
	if b.clockRate == 0 {
		return 0
	}

	return time.Duration(int64(delta) * int64(time.Second) / int64(b.clockRate))
}
