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

package source

import (
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk/jitter"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
)

const (
	gateClockRate = 48000
	// one Opus frame
	gateFrameDuration = 20 * time.Millisecond
	gateFrameRTP      = uint32(gateClockRate / 50)
	// the server hands over this many frames faster than real time
	gateBurstFrames  = 25
	gateBurstArrival = time.Millisecond
	// long enough for the wall-clock slew (1% of the gap per packet) to settle
	gateSteadyFrames = 500
	// media delivered for free during the handover
	gateBacklog = gateBurstFrames * (gateFrameDuration - gateBurstArrival)
)

// TestStartGateAnchorsPastTheBurst pins the behaviour the request types in
// shouldEnableStartGate depend on. A track anchored on the first packet of a
// buffered handover emits PTS short by the width of that handover for the rest
// of the session, which is what lands as an A/V offset when one track is
// anchored that way and another is not.
func TestStartGateAnchorsPastTheBurst(t *testing.T) {
	require.True(t, shouldEnableStartGate(&config.PipelineConfig{RequestType: types.RequestTypeTrackComposite}))
	require.True(t, shouldEnableStartGate(&config.PipelineConfig{RequestType: types.RequestTypeParticipant}))
	require.False(t, shouldEnableStartGate(&config.PipelineConfig{
		RequestType: types.RequestTypeMedia,
		Passthrough: true,
	}), "a passthrough request carries one track and has nothing to desync from")

	without := emitBurstThenSteady(t, false)
	with := emitBurstThenSteady(t, true)
	t.Logf("handover %s: lag without the gate %s, with the gate %s", gateBacklog, without, with)

	require.Greater(t, without, 400*time.Millisecond,
		"without the gate the emitted timeline should lag media time by about the %s handover, got %s",
		gateBacklog, without)
	require.Less(t, with.Abs(), 25*time.Millisecond,
		"with the gate the emitted timeline should track media time, got %s", with)
}

// emitBurstThenSteady feeds one track a burst of frames delivered faster than
// real time followed by frames at real-time cadence, and returns how far the
// last emitted PTS falls behind media time measured from the anchor packet.
func emitBurstThenSteady(t *testing.T, enableStartGate bool) time.Duration {
	t.Helper()

	opts := []synchronizer.SyncEngineOption{synchronizer.WithSyncEngineLogger(logger.GetLogger())}
	if enableStartGate {
		opts = append(opts, synchronizer.WithSyncEngineStartGate())
	}
	engine := synchronizer.NewSyncEngine(opts...)
	defer engine.End()

	trackSync := engine.AddTrack(gateTestTrack{}, "PA_test")
	defer trackSync.Close()

	var (
		primed   bool
		anchorTS uint32
		lastTS   uint32
		lastPTS  time.Duration
	)

	base := time.Now()
	emit := func(pkt jitter.ExtPacket) {
		pts, err := trackSync.GetPTS(pkt)
		require.NoError(t, err)
		lastTS, lastPTS = pkt.Timestamp, pts
	}

	for i := range gateBurstFrames + gateSteadyFrames {
		pkt := jitter.ExtPacket{
			ReceivedAt: base.Add(gateArrivalOffset(i)),
			Packet: &rtp.Packet{Header: rtp.Header{
				SequenceNumber: uint16(i),
				Timestamp:      uint32(i) * gateFrameRTP,
				SSRC:           uint32(gateTestSSRC),
			}},
		}

		if !primed {
			ready, _, done := trackSync.PrimeForStart(pkt)
			if !done {
				continue
			}
			require.NotEmpty(t, ready, "priming completed without packets")
			primed = true
			anchorTS = ready[0].Timestamp
			for _, p := range ready {
				emit(p)
			}
			continue
		}

		emit(pkt)
	}

	require.True(t, primed, "track never finished priming")

	mediaSinceAnchor := time.Duration(int64(lastTS-anchorTS)) * time.Second / gateClockRate
	return mediaSinceAnchor - lastPTS
}

// gateArrivalOffset places the first gateBurstFrames close together, as a
// server draining a buffer does, then at one frame per frame duration.
func gateArrivalOffset(i int) time.Duration {
	if i < gateBurstFrames {
		return time.Duration(i) * gateBurstArrival
	}
	return gateBurstFrames*gateBurstArrival + time.Duration(i-gateBurstFrames)*gateFrameDuration
}

const gateTestSSRC = webrtc.SSRC(0x1a2b3c4d)

type gateTestTrack struct{}

func (gateTestTrack) ID() string                { return "TR_startgate" }
func (gateTestTrack) Kind() webrtc.RTPCodecType { return webrtc.RTPCodecTypeAudio }
func (gateTestTrack) SSRC() webrtc.SSRC         { return gateTestSSRC }
func (gateTestTrack) Codec() webrtc.RTPCodecParameters {
	return webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: gateClockRate,
			Channels:  2,
		},
		PayloadType: 111,
	}
}
