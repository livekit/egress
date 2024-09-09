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

	extPkt := &buffer.ExtPacket{
		Packet:   pkt,
		Arrival:  time.Now().UnixNano(),
		Payload:  vp8Packet,
		KeyFrame: vp8Packet.IsKeyFrame,
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

// Null

type NullTranslator struct{}

func NewNullTranslator() Translator {
	return &NullTranslator{}
}

func (t *NullTranslator) Translate(_ *rtp.Packet) {}
