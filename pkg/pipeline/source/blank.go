package source

import (
	"encoding/binary"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"

	"github.com/livekit/livekit-egress/pkg/pipeline/params"
)

var (
	VP8KeyFrame8x8 = []byte{0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a, 0x08, 0x00, 0x08, 0x00, 0x00, 0x47, 0x08, 0x85, 0x85, 0x88, 0x85, 0x84, 0x88, 0x02, 0x02, 0x00, 0x0c, 0x0d, 0x60, 0x00, 0xfe, 0xff, 0xab, 0x50, 0x80}

	H264KeyFrame2x2SPS = []byte{0x67, 0x42, 0xc0, 0x1f, 0x0f, 0xd9, 0x1f, 0x88, 0x88, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04, 0x00, 0x00, 0x03, 0x00, 0xc8, 0x3c, 0x60, 0xc9, 0x20}
	H264KeyFrame2x2PPS = []byte{0x68, 0x87, 0xcb, 0x83, 0xcb, 0x20}
	H264KeyFrame2x2IDR = []byte{0x65, 0x88, 0x84, 0x0a, 0xf2, 0x62, 0x80, 0x00, 0xa7, 0xbe}

	H264KeyFrame2x2 = [][]byte{H264KeyFrame2x2SPS, H264KeyFrame2x2PPS, H264KeyFrame2x2IDR}
)

func getBlankFrame(
	track *webrtc.TrackRemote,
	codec params.MimeType,
	lastSequenceNumber uint16,
	lastTimestamp uint32,
) ([]*rtp.Packet, error) {

	pkts := make([]*rtp.Packet, 0, 7)
	for i := 0; i < 7; i++ {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Padding:        false,
				Marker:         true,
				PayloadType:    uint8(track.PayloadType()),
				SequenceNumber: lastSequenceNumber + uint16(i),
				Timestamp:      lastTimestamp + (uint32(i) * (track.Codec().ClockRate / (24000 / 1001))),
				SSRC:           uint32(track.SSRC()),
				CSRC:           []uint32{},
			},
		}

		switch codec {
		case params.MimeTypeVP8:
			// TODO: this is missing vp8 header
			pkt.Payload = make([]byte, 1+len(VP8KeyFrame8x8))
			pkt.Payload[0] = 0x10
			copy(pkt.Payload[1:], VP8KeyFrame8x8)

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

		pkts = append(pkts, pkt)
	}

	return pkts, nil
}
