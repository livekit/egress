package sdk

import (
	"io"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"

	"github.com/livekit/mediatransportutil"
	"github.com/livekit/protocol/logger"
)

const largePTSDiff = time.Millisecond * 20

// a single synchronizer is shared between audio and video writers
type synchronizer struct {
	sync.RWMutex

	firstPacket   int64
	ntpStart      time.Time
	endedAt       int64
	syncInfo      map[uint32]*trackInfo
	senderReports map[uint32]*rtcp.SenderReport
}

type trackInfo struct {
	sync.Mutex

	trackID    string
	firstRtpTS int64
	ptsDelay   int64
	clockRate  int64
	maxRTP     int64
}

func newSynchronizer() *synchronizer {
	return &synchronizer{
		syncInfo:      make(map[uint32]*trackInfo),
		senderReports: make(map[uint32]*rtcp.SenderReport),
	}
}

// initialize track sync info
func (c *synchronizer) addTrack(track *webrtc.TrackRemote) {
	c.Lock()
	c.syncInfo[uint32(track.SSRC())] = &trackInfo{
		trackID:   track.ID(),
		clockRate: int64(track.Codec().ClockRate),
	}
	c.Unlock()
}

// initialize offsets once the first rtp packet is received
func (c *synchronizer) firstPacketForTrack(pkt *rtp.Packet) {
	now := time.Now().UnixNano()

	c.Lock()
	if c.firstPacket == 0 {
		c.firstPacket = now
	}
	t := c.syncInfo[pkt.SSRC]
	c.Unlock()

	t.Lock()
	t.firstRtpTS = int64(pkt.Timestamp)
	t.ptsDelay = now - c.firstPacket
	t.Unlock()
}

func (c *synchronizer) getStartedAt() int64 {
	c.RLock()
	defer c.RUnlock()

	return c.firstPacket
}

func (c *synchronizer) onRTCP(packet rtcp.Packet) {
	switch pkt := packet.(type) {
	case *rtcp.SenderReport:
		c.Lock()
		defer c.Unlock()

		if c.endedAt != 0 {
			return
		}

		c.senderReports[pkt.SSRC] = pkt
		if c.ntpStart.IsZero() {
			if len(c.senderReports) < len(c.syncInfo) {
				// wait for at least one report per track
				return
			}

			// get the max ntp start time for all tracks
			var minNTPStart time.Time
			ntpStarts := make(map[uint32]time.Time)
			for _, report := range c.senderReports {
				t := c.syncInfo[report.SSRC]
				pts, _ := t.getPTS(int64(report.RTPTime))
				ntpStart := mediatransportutil.NtpTime(report.NTPTime).Time().Add(-pts)
				if minNTPStart.IsZero() || ntpStart.Before(minNTPStart) {
					minNTPStart = ntpStart
				}
				ntpStarts[report.SSRC] = ntpStart
			}
			c.ntpStart = minNTPStart

			// update pts delay so all ntp start times match
			for ssrc, ntpStart := range ntpStarts {
				t := c.syncInfo[ssrc]
				if diff := ntpStart.Sub(minNTPStart); diff != 0 {
					t.Lock()
					t.ptsDelay += int64(diff)
					t.Unlock()
				}
			}
		} else {
			t := c.syncInfo[pkt.SSRC]
			pts, _ := t.getPTS(int64(pkt.RTPTime))
			expected := mediatransportutil.NtpTime(pkt.NTPTime).Time().Sub(c.ntpStart)
			if pts != expected {
				diff := expected - pts
				if diff > largePTSDiff || diff < -largePTSDiff {
					logger.Warnw("high pts drift", nil, "trackID", t.trackID, "pts", pts, "diff", diff)
				}
				t.Lock()
				t.ptsDelay += int64(expected - pts)
				t.Unlock()
			}
		}
	}
}

// get pts for gstreamer buffer
func (c *synchronizer) getPTS(pkt *rtp.Packet) (time.Duration, error) {
	c.RLock()
	t := c.syncInfo[pkt.SSRC]
	c.RUnlock()

	return t.getPTS(int64(pkt.Timestamp))
}

func (t *trackInfo) getPTS(rtpTS int64) (time.Duration, error) {
	t.Lock()
	defer t.Unlock()

	if t.maxRTP != 0 && rtpTS > t.maxRTP {
		return 0, io.EOF
	}

	// RTP packet timestamps start at a random number, and increase according to clock rate (for example, with a
	// clock rate of 90kHz, the timestamp will increase by 90000 every second).
	// The GStreamer clock time also starts at a random number, and increases in nanoseconds.
	// The conversion is done by subtracting the initial RTP timestamp (tt.firstTS) from the current RTP timestamp
	// and multiplying by a conversion rate of (1e9 ns/s / clock rate).
	// Since the audio and video track might start pushing to their buffers at different times, we then add a
	// synced clock offset (tt.ptsDelay), which is always 0 for the first track, and fixes the video starting to play too
	// early if it's waiting for a key frame
	nanoSecondsElapsed := (rtpTS - t.firstRtpTS) * 1e9 / t.clockRate
	return time.Duration(nanoSecondsElapsed + t.ptsDelay), nil
}

// update maxRTP for each track
func (c *synchronizer) eos() {
	endTime := time.Now().UnixNano()

	c.Lock()
	defer c.Unlock()

	var maxDelay int64
	for _, t := range c.syncInfo {
		t.Lock()
		if t.ptsDelay > maxDelay {
			maxDelay = t.ptsDelay
		}
		t.Unlock()
	}
	c.endedAt = endTime + maxDelay

	// calculate max rtp timestamp for each track
	maxPTS := endTime - c.firstPacket + maxDelay
	for _, t := range c.syncInfo {
		t.Lock()
		maxNS := maxPTS - t.ptsDelay
		maxCycles := maxNS * 1e9 / t.clockRate
		t.maxRTP = maxCycles + t.firstRtpTS
		t.Unlock()
	}
}

func (c *synchronizer) getEndedAt() int64 {
	c.RLock()
	defer c.RUnlock()

	return c.endedAt
}
