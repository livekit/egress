package sdk

import (
	"encoding/binary"
	"io"
	"net"
	"time"

	"github.com/go-logr/logr"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/livekit-server/pkg/sfu"
	"github.com/livekit/livekit-server/pkg/sfu/buffer"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"
)

const (
	maxVideoLate = 1000 // nearly 2s for fhd video
	videoTimeout = time.Second * 2
	maxAudioLate = 200 // 4s for audio
	audioTimeout = time.Second * 4
)

var (
	VP8KeyFrame16x16 = []byte{0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a, 0x10, 0x00, 0x10, 0x00, 0x00, 0x47, 0x08, 0x85, 0x85, 0x88, 0x85, 0x84, 0x88, 0x02, 0x02, 0x00, 0x0c, 0x0d, 0x60, 0x00, 0xfe, 0xff, 0xab, 0x50, 0x80}

	H264KeyFrame2x2SPS = []byte{0x67, 0x42, 0xc0, 0x1f, 0x0f, 0xd9, 0x1f, 0x88, 0x88, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04, 0x00, 0x00, 0x03, 0x00, 0xc8, 0x3c, 0x60, 0xc9, 0x20}
	H264KeyFrame2x2PPS = []byte{0x68, 0x87, 0xcb, 0x83, 0xcb, 0x20}
	H264KeyFrame2x2IDR = []byte{0x65, 0x88, 0x84, 0x0a, 0xf2, 0x62, 0x80, 0x00, 0xa7, 0xbe}

	H264KeyFrame2x2 = [][]byte{H264KeyFrame2x2SPS, H264KeyFrame2x2PPS, H264KeyFrame2x2IDR}
)

type appWriter struct {
	logger      logger.Logger
	sb          *samplebuilder.SampleBuilder
	track       *webrtc.TrackRemote
	codec       params.MimeType
	src         *app.Source
	startTime   time.Time
	writeBlanks bool

	newSampleBuilder func() *samplebuilder.SampleBuilder
	writePLI         func()

	// a/v sync
	cs          *synchronizer
	clockSynced bool
	rtpOffset   int64
	ptsOffset   int64
	snOffset    uint16
	conversion  float64
	lastSN      uint16
	lastTS      uint32
	tsStep      uint32
	maxRTP      atomic.Int64

	// state
	muted        atomic.Bool
	playing      chan struct{}
	drain        chan struct{}
	drainTimeout time.Duration
	force        chan struct{}
	finished     chan struct{}

	// vp8
	firstPktPushed bool
	vp8Munger      *sfu.VP8Munger
}

func newAppWriter(
	track *webrtc.TrackRemote,
	codec params.MimeType,
	rp *lksdk.RemoteParticipant,
	l logger.Logger,
	src *app.Source,
	cs *synchronizer,
	playing chan struct{},
	writeBlanks bool,
) (*appWriter, error) {

	w := &appWriter{
		logger:      logger.Logger(logr.Logger(l).WithValues("trackID", track.ID(), "kind", track.Kind().String())),
		track:       track,
		codec:       codec,
		src:         src,
		writeBlanks: writeBlanks,
		cs:          cs,
		conversion:  1e9 / float64(track.Codec().ClockRate),
		playing:     playing,
		drain:       make(chan struct{}),
		force:       make(chan struct{}),
		finished:    make(chan struct{}),
	}

	var depacketizer rtp.Depacketizer
	var maxLate uint16
	switch codec {
	case params.MimeTypeVP8:
		depacketizer = &codecs.VP8Packet{}
		maxLate = maxVideoLate
		w.drainTimeout = videoTimeout
		w.writePLI = func() { rp.WritePLI(track.SSRC()) }
		w.vp8Munger = sfu.NewVP8Munger(w.logger)

	case params.MimeTypeH264:
		depacketizer = &codecs.H264Packet{}
		maxLate = maxVideoLate
		w.drainTimeout = videoTimeout
		w.writePLI = func() { rp.WritePLI(track.SSRC()) }

	case params.MimeTypeOpus:
		depacketizer = &codecs.OpusPacket{}
		maxLate = maxAudioLate
		w.drainTimeout = audioTimeout

	default:
		return nil, errors.ErrNotSupported(track.Codec().MimeType)
	}

	w.newSampleBuilder = func() *samplebuilder.SampleBuilder {
		return samplebuilder.New(
			maxLate, depacketizer, track.Codec().ClockRate,
			samplebuilder.WithPacketDroppedHandler(w.writePLI),
		)
	}
	w.sb = w.newSampleBuilder()

	go w.start()
	return w, nil
}

func (w *appWriter) start() {
	// always post EOS if the writer started playing
	defer func() {
		if w.isPlaying() {
			if flow := w.src.EndStream(); flow != gst.FlowOK && flow != gst.FlowFlushing {
				w.logger.Errorw("unexpected flow return", nil, "flowReturn", flow.String())
			}
		}

		close(w.finished)
	}()

	w.startTime = time.Now()

	for {
		select {
		case <-w.force:
			// force push remaining packets and quit
			_ = w.pushPackets(true)
			return

		default:
			// read next packet
			_ = w.track.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
			pkt, _, err := w.track.ReadRTP()
			if err != nil {
				if w.isDraining() {
					return
				}

				if w.muted.Load() {
					// switch to writing blank frames
					err = w.pushBlankFrames()
					if err == nil {
						continue
					}
				}

				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}

				// log non-EOF errors
				if !errors.Is(err, io.EOF) {
					w.logger.Errorw("could not read packet", err)
				}

				// force push remaining packets and quit
				_ = w.pushPackets(true)
				return
			}

			// sync offsets after first packet read
			// see comment in writeRTP below
			if !w.clockSynced {
				now := time.Now().UnixNano()
				startTime := w.cs.GetOrSetStartTime(now)
				w.ptsOffset = now - startTime
				w.rtpOffset = int64(pkt.Timestamp)
				w.clockSynced = true
			}

			// push packet to sample builder
			w.sb.Push(pkt)

			// push completed packets to appsrc
			if err = w.pushPackets(false); err != nil {
				if !errors.Is(err, io.EOF) {
					w.logger.Errorw("could not push buffers", err)
				}
				return
			}
		}
	}
}

func (w *appWriter) pushPackets(force bool) error {
	// buffers can only be pushed to the appsrc while in the playing state
	if !w.isPlaying() {
		return nil
	}

	if force {
		return w.push(w.sb.ForcePopPackets(), false)
	} else {
		return w.push(w.sb.PopPackets(), false)
	}
}

func (w *appWriter) pushBlankFrames() error {
	_ = w.pushPackets(true)

	// TODO: sample buffer has bug that it may pop old packet after pushPackets(true)
	//   recreated it to work now, will remove this when bug fixed
	w.sb = w.newSampleBuilder()

	if !w.writeBlanks {
		// wait until unmuted or closed
		ticker := time.NewTicker(time.Millisecond * 100)
		defer ticker.Stop()

		for {
			<-ticker.C
			if w.isDraining() || !w.muted.Load() {
				return nil
			}
		}
	}

	// expected difference between packet timestamps
	tsStep := w.tsStep
	if tsStep == 0 {
		w.logger.Debugw("no timestamp step, guessing")
		tsStep = w.track.Codec().ClockRate / (24000 / 1001)
	}

	// expected packet duration in nanoseconds
	frameDuration := time.Duration(float64(tsStep) * 1e9 / float64(w.track.Codec().ClockRate))
	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	for {
		if w.isDraining() {
			return nil
		}

		if !w.muted.Load() {
			// once unmuted, read next packet to determine stopping point
			// the blank frames should be ~500ms behind and need to fill the gap
			_ = w.track.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
			pkt, _, err := w.track.ReadRTP()
			if err != nil {
				// continue if read timeout
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}

				if !errors.Is(err, io.EOF) {
					w.logger.Errorw("could not read packet", err)
				}
				return err
			}

			maxTimestamp := pkt.Timestamp - tsStep
			for {
				ts := w.lastTS + tsStep
				if ts > maxTimestamp {
					// push packet to sample builder and return
					w.sb.Push(pkt)
					return nil
				}

				if err = w.pushBlankFrame(ts); err != nil {
					return err
				}
			}
		}

		<-ticker.C
		// push blank frame
		if err := w.pushBlankFrame(w.lastTS + tsStep); err != nil {
			return err
		}
	}
}

func (w *appWriter) pushBlankFrame(timestamp uint32) error {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Padding:        false,
			Marker:         true,
			PayloadType:    uint8(w.track.PayloadType()),
			SequenceNumber: w.lastSN + 1,
			Timestamp:      timestamp,
			SSRC:           uint32(w.track.SSRC()),
			CSRC:           []uint32{},
		},
	}
	w.snOffset++

	switch w.codec {
	case params.MimeTypeVP8:
		blankVP8 := w.vp8Munger.UpdateAndGetPadding(true)

		// 1x1 key frame
		// Used even when closing out a previous frame. Looks like receivers
		// do not care about content (it will probably end up being an undecodable
		// frame, but that should be okay as there are key frames following)
		payload := make([]byte, blankVP8.HeaderSize+len(VP8KeyFrame16x16))
		vp8Header := payload[:blankVP8.HeaderSize]
		err := blankVP8.MarshalTo(vp8Header)
		if err != nil {
			return err
		}

		copy(payload[blankVP8.HeaderSize:], VP8KeyFrame16x16)
		pkt.Payload = payload

	case params.MimeTypeH264:
		buf := make([]byte, 1462)
		offset := 0
		buf[0] = 0x18 // STAP-A
		offset++
		for _, payload := range H264KeyFrame2x2 {
			binary.BigEndian.PutUint16(buf[offset:], uint16(len(payload)))
			offset += 2
			copy(buf[offset:offset+len(payload)], payload)
			offset += len(payload)
		}

		pkt.Payload = buf[:offset]
	}

	if err := w.push([]*rtp.Packet{pkt}, true); err != nil {
		return err
	}

	return nil
}

func (w *appWriter) push(packets []*rtp.Packet, blankFrame bool) error {
	for _, pkt := range packets {
		if w.isDraining() && int64(pkt.Timestamp) >= w.maxRTP.Load() {
			return io.EOF
		}

		// record timestamp diff
		if w.tsStep == 0 && !blankFrame && w.lastTS != 0 && pkt.SequenceNumber == w.lastSN+1 {
			w.tsStep = pkt.Timestamp - w.lastTS
		}

		// record SN and TS
		w.lastSN = pkt.SequenceNumber
		w.lastTS = pkt.Timestamp

		if !blankFrame {
			// update sequence number
			pkt.SequenceNumber += w.snOffset
			w.translatePacket(pkt)
		}

		p, err := pkt.Marshal()
		if err != nil {
			return err
		}

		b := gst.NewBufferFromBytes(p)

		// RTP packet timestamps start at a random number, and increase according to clock rate (for example, with a
		// clock rate of 90kHz, the timestamp will increase by 90000 every second).
		// The GStreamer clock time also starts at a random number, and increases in nanoseconds.
		// The conversion is done by subtracting the initial RTP timestamp (w.rtpOffset) from the current RTP timestamp
		// and multiplying by a conversion rate of (1e9 ns/s / clock rate).
		// Since the audio and video track might start pushing to their buffers at different times, we then add a
		// synced clock offset (w.ptsOffset), which is always 0 for the first track, and fixes the video starting to play too
		// early if it's waiting for a key frame
		cyclesElapsed := int64(pkt.Timestamp) - w.rtpOffset
		nanoSecondsElapsed := int64(float64(cyclesElapsed) * w.conversion)
		b.SetPresentationTimestamp(time.Duration(nanoSecondsElapsed + w.ptsOffset))

		w.src.PushBuffer(b)
	}

	return nil
}

func (w *appWriter) translatePacket(pkt *rtp.Packet) {
	switch w.codec {
	case params.MimeTypeVP8:
		vp8Packet := buffer.VP8{}
		if err := vp8Packet.Unmarshal(pkt.Payload); err != nil {
			w.logger.Warnw("could not unmarshal VP8 packet", err)
			return
		}

		ep := &buffer.ExtPacket{
			Packet:   pkt,
			Arrival:  time.Now().UnixNano(),
			Payload:  vp8Packet,
			KeyFrame: vp8Packet.IsKeyFrame,
			VideoLayer: buffer.VideoLayer{
				Spatial:  -1,
				Temporal: int32(vp8Packet.TID),
			},
		}

		if !w.firstPktPushed {
			w.firstPktPushed = true
			w.vp8Munger.SetLast(ep)
		} else {
			tpVP8, err := w.vp8Munger.UpdateAndGet(ep, sfu.SequenceNumberOrderingContiguous, ep.Temporal)
			if err != nil {
				w.logger.Warnw("could not update VP8 packet", err)
				return
			}

			payload := pkt.Payload
			payload, err = w.translateVP8Packet(ep.Packet, &vp8Packet, tpVP8.Header, &payload)
			if err != nil {
				w.logger.Warnw("could not translate VP8 packet", err)
				return
			}
			pkt.Payload = payload
		}

	default:
		return
	}
}

func (w *appWriter) translateVP8Packet(pkt *rtp.Packet, incomingVP8 *buffer.VP8, translatedVP8 *buffer.VP8, outbuf *[]byte) ([]byte, error) {
	var buf []byte
	if outbuf == &pkt.Payload {
		buf = pkt.Payload
	} else {
		buf = (*outbuf)[:len(pkt.Payload)+translatedVP8.HeaderSize-incomingVP8.HeaderSize]

		srcPayload := pkt.Payload[incomingVP8.HeaderSize:]
		dstPayload := buf[translatedVP8.HeaderSize:]
		copy(dstPayload, srcPayload)
	}

	err := translatedVP8.MarshalTo(buf[:translatedVP8.HeaderSize])
	return buf, err
}

func (w *appWriter) isPlaying() bool {
	select {
	case <-w.playing:
		return true
	default:
		return false
	}
}

func (w *appWriter) isDraining() bool {
	select {
	case <-w.drain:
		return true
	default:
		return false
	}
}

func (w *appWriter) trackMuted() {
	w.logger.Debugw("track muted", "timestamp", time.Since(w.startTime).Seconds())
	w.muted.Store(true)
}

func (w *appWriter) trackUnmuted() {
	w.logger.Debugw("track unmuted", "timestamp", time.Since(w.startTime).Seconds())
	w.muted.Store(false)
	if w.writePLI != nil {
		w.writePLI()
	}
}

// sendEOS blocks until finished
func (w *appWriter) sendEOS() {
	select {
	case <-w.drain:
	default:
		w.logger.Debugw("draining")

		// get sync info
		startTime := w.cs.GetStartTime()
		endTime := w.cs.GetEndTime()
		delay := w.cs.GetDelay()

		// get the expected timestamp of the last packet
		nanoSecondsElapsed := endTime - startTime + delay - w.ptsOffset
		cyclesElapsed := int64(float64(nanoSecondsElapsed) / w.conversion)
		w.maxRTP.Store(cyclesElapsed + w.rtpOffset)

		// start draining
		close(w.drain)

		// wait until drainTimeout before force popping
		time.AfterFunc(w.drainTimeout, func() { close(w.force) })
	}

	// wait until finished
	<-w.finished
}
