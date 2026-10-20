// Copyright 2026 LiveKit, Inc.
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
	"strconv"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

// encodeOpusFrames returns n 20ms Opus packets of a mono tone
func encodeOpusFrames(t *testing.T, n int) [][]byte {
	t.Helper()
	// the encoder pads the stream with one more frame at EOS
	p, err := gst.NewPipelineFromString("audiotestsrc num-buffers=" + strconv.Itoa(n) + " samplesperbuffer=960 wave=sine ! audioconvert ! audioresample ! audio/x-raw,rate=48000,channels=1 ! opusenc frame-size=20 ! appsink name=enc sync=false")
	if err != nil {
		t.Skip("opusenc not available")
	}
	encEl, err := p.GetElementByName("enc")
	require.NoError(t, err)
	enc := app.SinkFromElement(encEl)
	require.NoError(t, p.SetState(gst.StatePlaying))
	defer func() { _ = p.SetState(gst.StateNull) }()

	var frames [][]byte
	for {
		sample := enc.PullSample()
		if sample == nil {
			break
		}
		frames = append(frames, append([]byte(nil), sample.GetBuffer().Bytes()...))
	}
	require.GreaterOrEqual(t, len(frames), n)
	return frames[:n]
}

// decodedSamples pushes RTP packets through rtpopusdepay and opusdec and returns the number
// of mono samples decoded
func decodedSamples(t *testing.T, push func(src *app.Source)) int64 {
	t.Helper()
	p, err := gst.NewPipelineFromString("appsrc name=src format=time caps=application/x-rtp,media=audio,encoding-name=OPUS,clock-rate=48000,payload=111 ! rtpopusdepay ! opusdec ! audioconvert ! audio/x-raw,format=S16LE,channels=1 ! appsink name=dec sync=false")
	if err != nil {
		t.Skip("opusdec not available")
	}
	srcEl, err := p.GetElementByName("src")
	require.NoError(t, err)
	decEl, err := p.GetElementByName("dec")
	require.NoError(t, err)
	src, dec := app.SrcFromElement(srcEl), app.SinkFromElement(decEl)
	require.NoError(t, p.SetState(gst.StatePlaying))
	defer func() { _ = p.SetState(gst.StateNull) }()

	push(src)
	src.EndStream()

	var samples int64
	for {
		sample := dec.PullSample()
		if sample == nil {
			break
		}
		samples += sample.GetBuffer().GetSize() / 2
	}
	return samples
}

func pushRTP(t *testing.T, src *app.Source, pkt *rtp.Packet, pts time.Duration) {
	t.Helper()
	data, err := pkt.Marshal()
	require.NoError(t, err)
	b := gst.NewBufferFromBytes(data)
	b.SetPresentationTimestamp(gst.ClockTime(uint64(pts)))
	require.Equal(t, gst.FlowOK, src.PushBuffer(b))
}

// The concealment packets the filler emits must decode to exactly the samples the gap
// spans; any difference would reach the tempo pacer as apparent clock skew.
func TestRTPGapFillerConcealmentDecodesExactly(t *testing.T) {
	gstInitOnce.Do(func() { gst.Init(nil) })

	const frames = 25
	encoded := encodeOpusFrames(t, frames)
	packet := func(i int, seq uint16, ts uint32) *rtp.Packet {
		return &rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 111, SSRC: 1, SequenceNumber: seq, Timestamp: ts},
			Payload: encoded[i],
		}
	}

	baseline := decodedSamples(t, func(src *app.Source) {
		for i := range frames {
			pushRTP(t, src, packet(i, uint16(i), uint32(i*960)), time.Duration(i)*20*time.Millisecond)
		}
	})
	require.Greater(t, baseline, int64(0))

	for _, gapSamples := range []int{960, 5 * 960, 50 * 960, 960 + 480, 480} {
		t.Run("gap of "+strconv.Itoa(gapSamples)+" samples", func(t *testing.T) {
			// the stream pauses after 10 packets, then resumes
			f := newRTPGapFiller(48000, opusGapCodec)
			var synthetic int64
			total := decodedSamples(t, func(src *app.Source) {
				for i := range frames {
					ts := uint32(i * 960)
					if i >= 10 {
						ts += uint32(gapSamples)
					}
					pkt := packet(i, uint16(i), ts)
					pts := time.Duration(ts) * time.Second / 48000
					require.NoError(t, f.Fill(pkt, pts, func(s *rtp.Packet, spts time.Duration) error {
						frame, count := opusFrameInfo(s.Payload)
						synthetic += int64(frame*48000/time.Second) * int64(count)
						pushRTP(t, src, s, spts)
						return nil
					}))
					pushRTP(t, src, pkt, pts)
					f.Advance(pkt, pts)
				}
			})
			require.Equal(t, int64(gapSamples), synthetic)
			require.Equal(t, baseline+int64(gapSamples), total, "the fill decodes to exactly the missing samples")
		})
	}
}
