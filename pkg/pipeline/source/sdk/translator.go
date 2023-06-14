package sdk

import (
	"encoding/binary"
	"time"

	"github.com/pion/rtp"

	"github.com/livekit/livekit-server/pkg/sfu/buffer"
	"github.com/livekit/livekit-server/pkg/sfu/codecmunger"
	"github.com/livekit/protocol/logger"
)

var (
	VP8KeyFrame16x16 = []byte{0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a, 0x10, 0x00, 0x10, 0x00, 0x00, 0x47, 0x08, 0x85, 0x85, 0x88, 0x85, 0x84, 0x88, 0x02, 0x02, 0x00, 0x0c, 0x0d, 0x60, 0x00, 0xfe, 0xff, 0xab, 0x50, 0x80}

	H264KeyFrame2x2SPS = []byte{0x67, 0x42, 0xc0, 0x1f, 0x0f, 0xd9, 0x1f, 0x88, 0x88, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04, 0x00, 0x00, 0x03, 0x00, 0xc8, 0x3c, 0x60, 0xc9, 0x20}
	H264KeyFrame2x2PPS = []byte{0x68, 0x87, 0xcb, 0x83, 0xcb, 0x20}
	H264KeyFrame2x2IDR = []byte{0x65, 0x88, 0x84, 0x0a, 0xf2, 0x62, 0x80, 0x00, 0xa7, 0xbe}

	H264KeyFrame2x2 = [][]byte{H264KeyFrame2x2SPS, H264KeyFrame2x2PPS, H264KeyFrame2x2IDR}
)

type Translator interface {
	Translate(*rtp.Packet)
	UpdateBlankFrame(*rtp.Packet) error
}

// VP8

type VP8Translator struct {
	logger logger.Logger

	firstPktPushed bool
	lastSN         uint16
	vp8Munger      *codecmunger.VP8
}

func NewVP8Translator(logger logger.Logger) *VP8Translator {
	return &VP8Translator{
		logger:    logger,
		vp8Munger: codecmunger.NewVP8(logger),
	}
}

func (t *VP8Translator) Translate(pkt *rtp.Packet) {
	defer func() {
		t.lastSN = pkt.SequenceNumber
	}()

	if len(pkt.Payload) == 0 {
		return
	}

	vp8Packet := buffer.VP8{}
	if err := vp8Packet.Unmarshal(pkt.Payload); err != nil {
		t.logger.Warnw("could not unmarshal VP8 packet", err)
		return
	}

	ep := &buffer.ExtPacket{
		Packet:   pkt,
		Arrival:  time.Now(),
		Payload:  vp8Packet,
		KeyFrame: vp8Packet.IsKeyFrame,
		VideoLayer: buffer.VideoLayer{
			Spatial:  -1,
			Temporal: int32(vp8Packet.TID),
		},
	}

	if !t.firstPktPushed {
		t.firstPktPushed = true
		t.vp8Munger.SetLast(ep)
	} else {
		tpVP8, err := t.vp8Munger.UpdateAndGet(ep, false, pkt.SequenceNumber != t.lastSN+1, ep.Temporal)
		if err != nil {
			t.logger.Warnw("could not update VP8 packet", err)
			return
		}
		pkt.Payload = translateVP8Packet(ep.Packet, &vp8Packet, tpVP8, &pkt.Payload)
	}
}

func translateVP8Packet(pkt *rtp.Packet, incomingVP8 *buffer.VP8, translatedVP8 []byte, outbuf *[]byte) []byte {
	buf := (*outbuf)[:len(pkt.Payload)+len(translatedVP8)-incomingVP8.HeaderSize]
	srcPayload := pkt.Payload[incomingVP8.HeaderSize:]
	dstPayload := buf[len(translatedVP8):]
	copy(dstPayload, srcPayload)

	copy(buf[:len(translatedVP8)], translatedVP8)
	return buf
}

func (t *VP8Translator) UpdateBlankFrame(pkt *rtp.Packet) error {
	blankVP8, err := t.vp8Munger.UpdateAndGetPadding(true)
	if err != nil {
		return err
	}

	// 16x16 key frame
	// Used even when closing out a previous frame. Looks like receivers
	// do not care about content (it will probably end up being an undecodable
	// frame, but that should be okay as there are key frames following)
	payload := make([]byte, len(blankVP8)+len(VP8KeyFrame16x16))
	copy(payload[:len(blankVP8)], blankVP8)
	copy(payload[len(blankVP8):], VP8KeyFrame16x16)
	pkt.Payload = payload
	return nil
}

// H264

type H264Translator struct{}

func NewH264Translator() Translator {
	return &H264Translator{}
}

func (t *H264Translator) Translate(_ *rtp.Packet) {}

func (t *H264Translator) UpdateBlankFrame(pkt *rtp.Packet) error {
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
	return nil
}

// Opus

type OpusTranslator struct{}

func NewOpusTranslator() Translator {
	return &OpusTranslator{}
}

func (t *OpusTranslator) Translate(_ *rtp.Packet) {}

func (t *OpusTranslator) UpdateBlankFrame(_ *rtp.Packet) error {
	return nil
}
