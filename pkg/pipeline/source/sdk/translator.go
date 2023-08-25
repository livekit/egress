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
	"time"

	"github.com/pion/rtp"

	"github.com/livekit/livekit-server/pkg/sfu/buffer"
	"github.com/livekit/livekit-server/pkg/sfu/codecmunger"
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

// Null

type NullTranslator struct{}

func NewNullTranslator() Translator {
	return &NullTranslator{}
}

func (t *NullTranslator) Translate(_ *rtp.Packet) {}
