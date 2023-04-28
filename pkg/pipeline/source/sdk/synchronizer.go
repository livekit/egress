package sdk

import (
	"github.com/livekit/server-sdk-go/pkg/synchronizer"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

type TrackSynchronizer struct {
	*synchronizer.TrackSynchronizer

	clockRate        uint32
	frameDurationRTP uint32 // RTP timestamp difference between frames (used for blank frame insertion)
	lastTS           uint32 // most recent RTP timestamp received

	lastSN   uint16 // previous sequence number
	snOffset uint16 // sequence number offset (increases with each blank frame inserted
}

func newTrackSynchronizer(t *synchronizer.TrackSynchronizer, track *webrtc.TrackRemote) *TrackSynchronizer {
	return &TrackSynchronizer{
		TrackSynchronizer: t,
		clockRate:         track.Codec().ClockRate,
	}
}

func (t *TrackSynchronizer) resetOffsets(pkt *rtp.Packet) {
	t.TrackSynchronizer.ResetOffsets(pkt.Timestamp, t.getFrameDurationRTP())

	t.snOffset = t.lastSN + 1 - pkt.SequenceNumber
	pkt.SequenceNumber = t.lastSN + 1
}

func (t *TrackSynchronizer) getFrameDurationRTP() uint32 {
	if frameDurationRTP := t.frameDurationRTP; frameDurationRTP != 0 {
		return frameDurationRTP
	}

	// no value recorded, assume 24 fps
	return uint32(float64(t.clockRate) / 24)
}
