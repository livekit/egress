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
	largePTSDrift          = time.Millisecond * 20
	massivePTSDrift        = time.Second
	wrapCheck       uint32 = 2147483648
	maxUInt32       int64  = 4294967296
)

// a single Synchronizer is shared between audio and video writers
type Synchronizer struct {
	sync.RWMutex

	firstPacket   int64
	onFirstPacket func()
	endedAt       int64

	psByIdentity map[string]*participantSynchronizer
	psByTrack    map[uint32]*participantSynchronizer
}

type participantSynchronizer struct {
	sync.Mutex

	ntpStart      time.Time
	syncInfo      map[uint32]*TrackSynchronizer
	senderReports map[uint32]*rtcp.SenderReport
}

type TrackSynchronizer struct {
	sync.Mutex

	trackID   string
	clockRate int64

	firstRTP  int64  // first RTP timestamp received
	lastRTP   uint32 // most recent RTP timestamp received
	rtpWrap   int64  // number of times RTP timestamp has wrapped
	rtpStep   uint32 // difference between RTP timestamps for sequential packets (used for blank frame insertion)
	maxRTP    uint32 // maximum accepted RTP timestamp after EOS
	ptsOffset int64  // presentation timestamp offset (used for a/v sync)

	lastSN   uint16 // previous sequence number
	snOffset uint16 // sequence number offset (increases with each blank frame inserted)

	largeDrift time.Duration // track massive PTS drift, in case it's correct
}

func NewSynchronizer(onFirstPacket func()) *Synchronizer {
	return &Synchronizer{
		onFirstPacket: onFirstPacket,
		psByIdentity:  make(map[string]*participantSynchronizer),
		psByTrack:     make(map[uint32]*participantSynchronizer),
	}
}

// addTrack creates track sync info
func (s *Synchronizer) AddTrack(track *webrtc.TrackRemote, identity string) *TrackSynchronizer {
	t := &TrackSynchronizer{
		trackID:   track.ID(),
		clockRate: int64(track.Codec().ClockRate),
	}
	s.Lock()
	p := s.psByIdentity[identity]
	if p == nil {
		p = &participantSynchronizer{
			syncInfo:      make(map[uint32]*TrackSynchronizer),
			senderReports: make(map[uint32]*rtcp.SenderReport),
		}
		s.psByIdentity[identity] = p
	}
	ssrc := uint32(track.SSRC())
	s.psByTrack[ssrc] = p
	s.Unlock()

	p.Lock()
	p.syncInfo[ssrc] = t
	p.Unlock()

	return t
}

// firstPacketForTrack initializes offsets
func (s *Synchronizer) firstPacketForTrack(pkt *rtp.Packet) {
	now := time.Now().UnixNano()

	s.Lock()
	if s.firstPacket == 0 {
		s.firstPacket = now
		s.onFirstPacket()
	}
	p := s.psByTrack[pkt.SSRC]
	s.Unlock()

	p.Lock()
	t := p.syncInfo[pkt.SSRC]
	p.Unlock()

	t.Lock()
	t.firstRTP = int64(pkt.Timestamp)
	t.ptsOffset = now - s.firstPacket
	t.Unlock()
}

func (s *Synchronizer) GetStartedAt() int64 {
	s.RLock()
	defer s.RUnlock()

	return s.firstPacket
}

// OnRTCP syncs a/v using sender reports
func (s *Synchronizer) OnRTCP(packet rtcp.Packet) {
	switch pkt := packet.(type) {
	case *rtcp.SenderReport:
		s.Lock()
		p := s.psByTrack[pkt.SSRC]
		endedAt := s.endedAt
		s.Unlock()

		if endedAt != 0 {
			return
		}

		p.Lock()
		defer p.Unlock()

		p.senderReports[pkt.SSRC] = pkt
		if p.ntpStart.IsZero() {
			if len(p.senderReports) < len(p.syncInfo) {
				// wait for at least one report per track
				return
			}

			// get the max ntp start time for all tracks
			var minNTPStart time.Time
			ntpStarts := make(map[uint32]time.Time)
			for _, report := range p.senderReports {
				t := p.syncInfo[report.SSRC]
				pts, _ := t.getPTS(report.RTPTime)
				ntpStart := mediatransportutil.NtpTime(report.NTPTime).Time().Add(-pts)
				if minNTPStart.IsZero() || ntpStart.Before(minNTPStart) {
					minNTPStart = ntpStart
				}
				ntpStarts[report.SSRC] = ntpStart
			}
			p.ntpStart = minNTPStart

			// update pts delay so all ntp start times match
			for ssrc, ntpStart := range ntpStarts {
				t := p.syncInfo[ssrc]
				if diff := ntpStart.Sub(minNTPStart); diff != 0 {
					t.Lock()
					t.ptsOffset += int64(diff)
					t.Unlock()
				}
			}
		} else {
			p.syncInfo[pkt.SSRC].onSenderReport(pkt, p.ntpStart)
		}
	}
}

// onSenderReport handles pts adjustments for a track
func (t *TrackSynchronizer) onSenderReport(pkt *rtcp.SenderReport, ntpStart time.Time) {
	pts, _ := t.getPTS(pkt.RTPTime)
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
func (t *TrackSynchronizer) getPTS(ts uint32) (time.Duration, error) {
	t.Lock()
	defer t.Unlock()

	if t.maxRTP != 0 && ts > t.maxRTP {
		return 0, io.EOF
	}

	// RTP packet timestamps start at a random number, and increase according to clock rate (for example, with a
	// clock rate of 90kHz, the timestamp will increase by 90000 every second).
	// The GStreamer clock time also starts at a random number, and increases in nanoseconds.
	// The conversion is done by subtracting the initial RTP timestamp (tt.firstTS) from the current RTP timestamp
	// and multiplying by a conversion rate of (1e9 ns/s / clock rate).
	// Since the audio and video track might start pushing to their buffers at different times, we then add a
	// synced clock offset (tt.ptsOffset), which is always 0 for the first track, and fixes the video starting to Play too
	// early if it's waiting for a key frame
	rtpWrap := t.rtpWrap
	if ts < wrapCheck && t.lastRTP > wrapCheck {
		rtpWrap++
	}
	rtpTS := int64(ts) + (rtpWrap * maxUInt32)
	nanoSecondsElapsed := (rtpTS - t.firstRTP) * 1e9 / t.clockRate
	return time.Duration(nanoSecondsElapsed + t.ptsOffset), nil
}

// end updates maxRTP for each track
func (s *Synchronizer) End() {
	endTime := time.Now().UnixNano()

	s.Lock()
	defer s.Unlock()

	var maxDelay int64
	for _, p := range s.psByIdentity {
		p.Lock()
		for _, t := range p.syncInfo {
			t.Lock()
			if t.ptsOffset > maxDelay {
				maxDelay = t.ptsOffset
			}
			t.Unlock()
		}
		p.Unlock()
	}

	s.endedAt = endTime + maxDelay
	maxPTS := endTime - s.firstPacket + maxDelay

	for _, p := range s.psByIdentity {
		p.Lock()
		for _, t := range p.syncInfo {
			t.Lock()
			maxNS := maxPTS - t.ptsOffset
			maxCycles := maxNS * 1e9 / t.clockRate
			maxRTP := maxCycles + t.firstRTP
			for maxRTP >= maxUInt32 {
				maxRTP -= maxUInt32
			}
			t.maxRTP = uint32(maxRTP)
			t.Unlock()
		}
		p.Unlock()
	}
}

func (s *Synchronizer) GetEndedAt() int64 {
	s.RLock()
	defer s.RUnlock()

	return s.endedAt
}
