package sdk

import (
	"encoding/binary"
	"io"
	"net"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
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

type AppWriter struct {
	logger      logger.Logger
	sb          *samplebuilder.SampleBuilder
	track       *webrtc.TrackRemote
	identity    string
	codec       types.MimeType
	src         *app.Source
	startTime   time.Time
	writeBlanks bool

	newSampleBuilder func() *samplebuilder.SampleBuilder
	writePLI         func()

	// a/v sync
	sync *Synchronizer
	*TrackSynchronizer

	// state
	muted        atomic.Bool
	playing      chan struct{}
	draining     chan struct{}
	drainTimeout time.Duration
	force        chan struct{}
	finished     chan struct{}

	// vp8
	firstPktPushed bool
	vp8Munger      *sfu.VP8Munger
}

func NewAppWriter(
	track *webrtc.TrackRemote,
	rp *lksdk.RemoteParticipant,
	codec types.MimeType,
	appSrcName string,
	sync *Synchronizer,
	syncInfo *TrackSynchronizer,
	writeBlanks bool,
) (*AppWriter, error) {
	src, err := gst.NewElementWithName("appsrc", appSrcName)
	if err != nil {
		logger.Errorw("could not create appsrc", err)
		return nil, err
	}

	w := &AppWriter{
		logger:            logger.GetLogger().WithValues("trackID", track.ID(), "kind", track.Kind().String()),
		track:             track,
		identity:          rp.Identity(),
		codec:             codec,
		src:               app.SrcFromElement(src),
		writeBlanks:       writeBlanks,
		sync:              sync,
		TrackSynchronizer: syncInfo,
		playing:           make(chan struct{}),
		draining:          make(chan struct{}),
		force:             make(chan struct{}),
		finished:          make(chan struct{}),
	}

	var depacketizer rtp.Depacketizer
	var maxLate uint16
	switch codec {
	case types.MimeTypeVP8:
		depacketizer = &codecs.VP8Packet{}
		maxLate = maxVideoLate
		w.drainTimeout = videoTimeout
		w.writePLI = func() { rp.WritePLI(track.SSRC()) }
		w.vp8Munger = sfu.NewVP8Munger(w.logger)

	case types.MimeTypeH264:
		depacketizer = &codecs.H264Packet{}
		maxLate = maxVideoLate
		w.drainTimeout = videoTimeout
		w.writePLI = func() { rp.WritePLI(track.SSRC()) }

	case types.MimeTypeOpus:
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

func (w *AppWriter) start() {
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
	first := true

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
			if first {
				w.sync.firstPacketForTrack(pkt)
				first = false
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

func (w *AppWriter) pushPackets(force bool) error {
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

func (w *AppWriter) pushBlankFrames() error {
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
	tsStep := w.rtpStep
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
				ts := w.lastRTP + tsStep
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
		if err := w.pushBlankFrame(w.lastRTP + tsStep); err != nil {
			return err
		}
	}
}

func (w *AppWriter) pushBlankFrame(timestamp uint32) error {
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
	case types.MimeTypeVP8:
		blankVP8 := w.vp8Munger.UpdateAndGetPadding(true)

		// 16x16 key frame
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

	case types.MimeTypeH264:
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

func (w *AppWriter) push(packets []*rtp.Packet, blankFrame bool) error {
	for _, pkt := range packets {
		// record timestamp diff
		if w.rtpStep == 0 && !blankFrame && w.lastRTP != 0 && pkt.SequenceNumber == w.lastSN+1 {
			w.rtpStep = pkt.Timestamp - w.lastRTP
		}

		// record SN and TS
		w.lastSN = pkt.SequenceNumber
		if w.lastRTP > wrapCheck && pkt.Timestamp < wrapCheck {
			w.rtpWrap++
		}
		w.lastRTP = pkt.Timestamp

		if !blankFrame {
			// update sequence number
			pkt.SequenceNumber += w.snOffset
			w.translatePacket(pkt)
		}

		// will return io.EOF if EOS has been sent
		pts, err := w.getPTS(pkt.Timestamp)
		if err != nil {
			return err
		}

		p, err := pkt.Marshal()
		if err != nil {
			return err
		}

		b := gst.NewBufferFromBytes(p)
		b.SetPresentationTimestamp(pts)
		w.src.PushBuffer(b)
	}

	return nil
}

func (w *AppWriter) translatePacket(pkt *rtp.Packet) {
	switch w.codec {
	case types.MimeTypeVP8:
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

func (w *AppWriter) translateVP8Packet(pkt *rtp.Packet, incomingVP8 *buffer.VP8, translatedVP8 *buffer.VP8, outbuf *[]byte) ([]byte, error) {
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

func (w *AppWriter) Play() {
	select {
	case <-w.playing:
		return
	default:
		close(w.playing)
	}
}

func (w *AppWriter) isPlaying() bool {
	select {
	case <-w.playing:
		return true
	default:
		return false
	}
}

func (w *AppWriter) TrackMuted() {
	w.logger.Debugw("track muted", "timestamp", time.Since(w.startTime).Seconds())
	w.muted.Store(true)
}

func (w *AppWriter) TrackUnmuted() {
	w.logger.Debugw("track unmuted", "timestamp", time.Since(w.startTime).Seconds())
	w.muted.Store(false)
	if w.writePLI != nil {
		w.writePLI()
	}
}

// Drain blocks until finished
func (w *AppWriter) Drain(force bool) {
	select {
	case <-w.draining:
	default:
		w.logger.Debugw("draining")

		if force {
			close(w.force)
		} else {
			// wait until drainTimeout before force popping
			time.AfterFunc(w.drainTimeout, func() { close(w.force) })
		}

		close(w.draining)
	}

	// wait until finished
	<-w.finished
}

func (w *AppWriter) isDraining() bool {
	select {
	case <-w.draining:
		return true
	default:
		return false
	}
}
