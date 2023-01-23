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

const (
	largePTSDrift   = time.Millisecond * 20
	massivePTSDrift = time.Second
)

// a single synchronizer is shared between audio and video writers
type synchronizer struct {
	sync.RWMutex

	firstPacket   int64
	onFirstPacket func()
	ntpStart      time.Time
	endedAt       int64
	syncInfo      map[uint32]*syncInfo
	senderReports map[uint32]*rtcp.SenderReport
}

// TODO: handle (rare) RTP timestamp wrap
type syncInfo struct {
	sync.Mutex

	trackID   string
	clockRate int64

	firstRTP  int64  // first RTP timestamp received
	lastRTP   uint32 // most recent RTP timestamp received
	rtpStep   uint32 // difference between RTP timestamps for sequential packets (used for blank frame insertion)
	ptsOffset int64  // presentation timestamp offset (used for a/v sync)
	maxRTP    int64  // maximum accepted RTP timestamp after EOS

	lastSN   uint16 // previous sequence number
	snOffset uint16 // sequence number offset (increases with each blank frame inserted)

	largeDrift time.Duration // track massive PTS drift, in case it's correct
}

func newSynchronizer(onFirstPacket func()) *synchronizer {
	return &synchronizer{
		onFirstPacket: onFirstPacket,
		syncInfo:      make(map[uint32]*syncInfo),
		senderReports: make(map[uint32]*rtcp.SenderReport),
	}
}

// addTrack creates track sync info
func (c *synchronizer) addTrack(track *webrtc.TrackRemote) *syncInfo {
	t := &syncInfo{
		trackID:   track.ID(),
		clockRate: int64(track.Codec().ClockRate),
	}
	c.Lock()
	c.syncInfo[uint32(track.SSRC())] = t
	c.Unlock()
	return t
}

// firstPacketForTrack initializes offsets
func (c *synchronizer) firstPacketForTrack(pkt *rtp.Packet) {
	now := time.Now().UnixNano()

	c.Lock()
	if c.firstPacket == 0 {
		c.firstPacket = now
		c.onFirstPacket()
	}
	t := c.syncInfo[pkt.SSRC]
	c.Unlock()

	t.Lock()
	t.firstRTP = int64(pkt.Timestamp)
	t.ptsOffset = now - c.firstPacket
	t.Unlock()
}

func (c *synchronizer) getStartedAt() int64 {
	c.RLock()
	defer c.RUnlock()

	return c.firstPacket
}

// onRTCP syncs a/v using sender reports
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
					t.ptsOffset += int64(diff)
					t.Unlock()
				}
			}
		} else {
			c.syncInfo[pkt.SSRC].onSenderReport(pkt, c.ntpStart)
		}
	}
}

// onSenderReport handles pts adjustments for a track
func (t *syncInfo) onSenderReport(pkt *rtcp.SenderReport, ntpStart time.Time) {
	pts, _ := t.getPTS(int64(pkt.RTPTime))
	expected := mediatransportutil.NtpTime(pkt.NTPTime).Time().Sub(ntpStart)
	if pts != expected {
		diff := expected - pts
		apply := true
		t.Lock()
		if absGreater(diff, largePTSDrift) {
			logger.Warnw("high pts drift", nil, "trackID", t.trackID, "pts", pts, "diff", diff)
			if absGreater(diff, massivePTSDrift) {
				// if it's the first time seeing a massive drift, ignore it
				if t.largeDrift == 0 || absGreater(diff-t.largeDrift, largePTSDrift) {
					t.largeDrift = diff
					apply = false
				}
			}
		}
		if apply {
			t.ptsOffset += int64(diff)
		}
		t.Unlock()
	}
}

func absGreater(value, max time.Duration) bool {
	return value > max || value < -max
}

// getPTS calculates presentation timestamp from RTP timestamp
func (t *syncInfo) getPTS(rtpTS int64) (time.Duration, error) {
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
	// synced clock offset (tt.ptsOffset), which is always 0 for the first track, and fixes the video starting to play too
	// early if it's waiting for a key frame
	nanoSecondsElapsed := (rtpTS - t.firstRTP) * 1e9 / t.clockRate
	return time.Duration(nanoSecondsElapsed + t.ptsOffset), nil
}

// eos updates maxRTP for each track
func (c *synchronizer) eos() {
	endTime := time.Now().UnixNano()

	c.Lock()
	defer c.Unlock()

	var maxDelay int64
	for _, t := range c.syncInfo {
		t.Lock()
		if t.ptsOffset > maxDelay {
			maxDelay = t.ptsOffset
		}
		t.Unlock()
	}
	c.endedAt = endTime + maxDelay

	// calculate max rtp timestamp for each track
	maxPTS := endTime - c.firstPacket + maxDelay
	for _, t := range c.syncInfo {
		t.Lock()
		maxNS := maxPTS - t.ptsOffset
		maxCycles := maxNS * 1e9 / t.clockRate
		t.maxRTP = maxCycles + t.firstRTP
		t.Unlock()
	}
}

func (c *synchronizer) getEndedAt() int64 {
	c.RLock()
	defer c.RUnlock()

	return c.endedAt
}
