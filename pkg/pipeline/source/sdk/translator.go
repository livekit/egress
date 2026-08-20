// Copyright 2023 LiveKit, Inc.
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

package sdk

import (
	"bytes"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"

	"github.com/livekit/livekit-server/pkg/sfu/buffer"
	"github.com/livekit/livekit-server/pkg/sfu/codecmunger"
	"github.com/livekit/mediatransportutil/pkg/codec"
	"github.com/livekit/protocol/logger"
)

type Translator interface {
	Translate(*rtp.Packet)
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

	vp8Packet := codec.VP8{}
	if err := vp8Packet.Unmarshal(pkt.Payload); err != nil {
		t.logger.Warnw("could not unmarshal VP8 packet", err)
		return
	}

	extPkt := &buffer.ExtPacket{
		Packet:   pkt,
		Arrival:  time.Now().UnixNano(),
		Payload:  vp8Packet,
		IsKeyFrame: vp8Packet.IsKeyFrame,
		VideoLayer: buffer.VideoLayer{
			Spatial:  -1,
			Temporal: int32(vp8Packet.TID),
		},
	}

	if !t.firstPktPushed {
		t.firstPktPushed = true
		t.vp8Munger.SetLast(extPkt)
	} else {
		payload := make([]byte, 1460)
		incomingHeaderSize, header, err := t.vp8Munger.UpdateAndGet(extPkt, false, pkt.SequenceNumber != t.lastSN+1, extPkt.Temporal)
		if err != nil {
			t.logger.Warnw("could not update VP8 packet", err)
			return
		}
		copy(payload, header)
		n := copy(payload[len(header):], extPkt.Packet.Payload[incomingHeaderSize:])
		pkt.Payload = payload[:len(header)+n]
	}
}

// VP9

// VP9Translator strips LKTS packet trailers (LiveKit packet_trailer feature)
// from the tail of each VP9 layer frame. SVC publishers append one trailer per
// spatial layer; the SFU strips only the marker-packet tail, so per-layer
// trailers embedded mid-picture reach the decoder and corrupt every frame
// (GstVP9Dec "corrupt frame" / "no valid frames decoded"). The envelope is
// [trailer_len^0xFF (1B)]["LKTS" (4B)] at the payload tail of each
// end-of-layer-frame (E-bit) packet.

const lktsEnvelopeSize = 5

var lktsMagic = []byte{'L', 'K', 'T', 'S'}

type VP9Translator struct{}

func NewVP9Translator() Translator {
	return &VP9Translator{}
}

func (t *VP9Translator) Translate(pkt *rtp.Packet) {
	payload := pkt.Payload
	if len(payload) < lktsEnvelopeSize {
		return
	}
	var vp9 codecs.VP9Packet
	if _, err := vp9.Unmarshal(payload); err != nil {
		return
	}
	if !vp9.E {
		// A trailer can only sit at the end of a layer frame.
		return
	}
	if !bytes.Equal(payload[len(payload)-4:], lktsMagic) {
		return
	}
	trailerLen := int(payload[len(payload)-5] ^ 0xFF)
	if trailerLen < lktsEnvelopeSize || trailerLen > len(vp9.Payload) {
		return
	}
	pkt.Payload = payload[:len(payload)-trailerLen]
}

// Null

type NullTranslator struct{}

func NewNullTranslator() Translator {
	return &NullTranslator{}
}

func (t *NullTranslator) Translate(_ *rtp.Packet) {}
