package sdk

import (
	"encoding/binary"
	"io"
	"net"
	"time"

	"github.com/frostbyte73/core"
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
	"github.com/livekit/server-sdk-go/pkg/synchronizer"
)

type state int

const (
	statePlaying state = iota
	stateMuted
	stateUnmuting
)

const (
	maxVideoLate      = 1000 // nearly 2s for fhd video
	videoTimeout      = time.Second * 2
	maxAudioLate      = 200 // 4s for audio
	audioTimeout      = time.Second * 4
	errBufferTooSmall = "buffer too small"
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
	sendPLI          func()

	// a/v sync
	sync *synchronizer.Synchronizer
	*synchronizer.TrackSynchronizer
	lastPTS time.Duration

	// state
	state        state
	initialized  bool
	ticker       *time.Ticker
	muted        atomic.Bool
	playing      core.Fuse
	draining     core.Fuse
	drainTimeout time.Duration
	endStream    core.Fuse
	finished     core.Fuse

	// vp8
	firstPktPushed bool
	vp8Munger      *sfu.VP8Munger
}

func NewAppWriter(
	track *webrtc.TrackRemote,
	rp *lksdk.RemoteParticipant,
	codec types.MimeType,
	src *app.Source,
	sync *synchronizer.Synchronizer,
	syncInfo *synchronizer.TrackSynchronizer,
	writeBlanks bool,
) (*AppWriter, error) {
	w := &AppWriter{
		logger:            logger.GetLogger().WithValues("trackID", track.ID(), "kind", track.Kind().String()),
		track:             track,
		identity:          rp.Identity(),
		codec:             codec,
		src:               src,
		writeBlanks:       writeBlanks,
		sync:              sync,
		TrackSynchronizer: syncInfo,
		playing:           core.NewFuse(),
		draining:          core.NewFuse(),
		endStream:         core.NewFuse(),
		finished:          core.NewFuse(),
	}

	var depacketizer rtp.Depacketizer
	var maxLate uint16
	switch codec {
	case types.MimeTypeVP8:
		depacketizer = &codecs.VP8Packet{}
		maxLate = maxVideoLate
		w.drainTimeout = videoTimeout
		w.sendPLI = func() { rp.WritePLI(track.SSRC()) }
		w.vp8Munger = sfu.NewVP8Munger(w.logger)

	case types.MimeTypeH264:
		depacketizer = &codecs.H264Packet{}
		maxLate = maxVideoLate
		w.drainTimeout = videoTimeout
		w.sendPLI = func() { rp.WritePLI(track.SSRC()) }

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
			samplebuilder.WithPacketDroppedHandler(w.sendPLI),
		)
	}
	w.sb = w.newSampleBuilder()

	go w.run()
	return w, nil
}

func (w *AppWriter) Play() {
	w.playing.Break()
}

func (w *AppWriter) SetTrackMuted(muted bool) {
	w.muted.Store(muted)
	if muted {
		w.logger.Debugw("track muted", "timestamp", time.Since(w.startTime).Seconds())
	} else {
		w.logger.Debugw("track unmuted", "timestamp", time.Since(w.startTime).Seconds())
		if w.sendPLI != nil {
			w.sendPLI()
		}
	}
}

// Drain blocks until finished
func (w *AppWriter) Drain(force bool) {
	w.draining.Once(func() {
		w.logger.Debugw("draining")

		if force {
			w.endStream.Break()
		} else {
			// wait until drainTimeout before force popping
			time.AfterFunc(w.drainTimeout, w.endStream.Break)
		}
	})

	// wait until finished
	<-w.finished.Watch()
}

func (w *AppWriter) run() {
	w.startTime = time.Now()

	for !w.endStream.IsBroken() {
		switch w.state {
		case statePlaying:
			w.handlePlaying()
		case stateMuted:
			w.handleMuted()
		case stateUnmuting:
			w.handleUnmute()
		}
	}

	// clean up
	_ = w.pushSamples(true)
	if w.playing.IsBroken() {
		if flow := w.src.EndStream(); flow != gst.FlowOK && flow != gst.FlowFlushing {
			w.logger.Errorw("unexpected flow return", nil, "flowReturn", flow.String())
		}
	}
	w.finished.Break()
}

func (w *AppWriter) handlePlaying() {
	// read next packet
	_ = w.track.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
	pkt, _, err := w.track.ReadRTP()
	if err != nil {
		w.handleReadError(err)
		return
	}

	// initialize track synchronizer
	if !w.initialized {
		w.Initialize(pkt)
		w.initialized = true
	}

	// push packet to sample builder
	w.sb.Push(pkt)

	// push completed packets to appsrc
	if err = w.pushSamples(false); err != nil {
		w.endStream.Break()
	}
}

func (w *AppWriter) handleMuted() {
	switch {
	case w.draining.IsBroken():
		w.ticker.Stop()
		w.endStream.Break()

	case !w.muted.Load():
		w.ticker.Stop()
		if w.writeBlanks {
			w.state = stateUnmuting
		} else {
			w.state = statePlaying
		}

	default:
		<-w.ticker.C
		if w.writeBlanks {
			_, err := w.insertBlankFrame(nil)
			if err != nil {
				w.ticker.Stop()
				w.endStream.Break()
			}
		}
	}
}

func (w *AppWriter) handleUnmute() {
	_ = w.track.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
	pkt, _, err := w.track.ReadRTP()
	if err != nil {
		w.handleReadError(err)
		return
	}

	// the blank frames will be ~500ms behind and need to fill the gap
	for !w.endStream.IsBroken() {
		ok, err := w.insertBlankFrame(pkt)
		if err != nil {
			w.endStream.Break()
			return
		} else if !ok {
			// push packet to sample builder and return
			w.sb.Push(pkt)
			w.state = statePlaying
			return
		}
	}
}

func (w *AppWriter) handleReadError(err error) {
	if w.draining.IsBroken() {
		w.endStream.Break()
		return
	}

	// continue on buffer too small error
	if err.Error() == errBufferTooSmall {
		w.logger.Warnw("read error", err)
		return
	}

	// check if muted
	if w.muted.Load() {
		_ = w.pushSamples(true)

		// TODO: sample buffer has bug that it may pop old packet after pushPackets(true)
		//   recreated it to work now, will remove this when bug fixed
		w.sb = w.newSampleBuilder()
		w.ticker = time.NewTicker(w.GetFrameDuration())
		w.state = stateMuted
		return
	}

	// continue on timeout
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return
	}

	// log non-EOF errors
	if !errors.Is(err, io.EOF) {
		w.logger.Errorw("could not read packet", err)
	}

	// end stream
	w.endStream.Break()
}

func (w *AppWriter) insertBlankFrame(next *rtp.Packet) (bool, error) {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:     2,
			Padding:     false,
			Marker:      true,
			PayloadType: uint8(w.track.PayloadType()),
			SSRC:        uint32(w.track.SSRC()),
			CSRC:        []uint32{},
		},
	}

	var pts time.Duration
	if next != nil {
		var ok bool
		pts, ok = w.InsertFrameBefore(pkt, next)
		if !ok {
			return false, nil
		}
	} else {
		pts = w.InsertFrame(pkt)
	}

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
			return false, err
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

	if err := w.pushPacket(pkt, pts); err != nil {
		return false, err
	}

	return true, nil
}

func (w *AppWriter) pushSamples(force bool) error {
	// buffers can only be pushed to the appsrc while in the playing state
	if !w.playing.IsBroken() {
		return nil
	}

	var pkts []*rtp.Packet
	if force {
		pkts = w.sb.ForcePopPackets()
	} else {
		pkts = w.sb.PopPackets()
	}

	for _, pkt := range pkts {
		w.translatePacket(pkt)

		// get PTS
		pts, err := w.GetPTS(pkt)
		if err != nil {
			return err
		}

		if err = w.pushPacket(pkt, pts); err != nil {
			return err
		}
	}

	return nil
}

func (w *AppWriter) pushPacket(pkt *rtp.Packet, pts time.Duration) error {
	if pts < w.lastPTS {
		// don't push backwards pts
		w.logger.Warnw("backwards pts", nil, "pts", pts, "lastPTS", w.lastPTS)
		return nil
	}

	p, err := pkt.Marshal()
	if err != nil {
		w.logger.Errorw("could not marshal packet", err)
		return err
	}

	b := gst.NewBufferFromBytes(p)
	b.SetPresentationTimestamp(pts)
	w.lastPTS = pts

	if flow := w.src.PushBuffer(b); flow != gst.FlowOK {
		w.logger.Infow("unexpected flow return", "flow", flow)
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
