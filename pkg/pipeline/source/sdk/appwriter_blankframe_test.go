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
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/livekit-server/pkg/sfu"
	"github.com/livekit/media-sdk/jitter"
	"github.com/livekit/mediatransportutil/pkg/codec"
	"github.com/livekit/protocol/logger"
)

const (
	blankFrameRTPClockRate = 90000
	blankFrameFPS          = 30
	// the SFU sends blank frames for RTPBlankFramesCloseSeconds at the track's frame rate
	sfuBlankFrameCount = 6
)

// rtpClockPTS is a TrackSync that derives PTS from the RTP timestamp.
type rtpClockPTS struct {
	first    uint32
	hasFirst bool
}

func (s *rtpClockPTS) PrimeForStart(pkt jitter.ExtPacket) ([]jitter.ExtPacket, int, bool) {
	return []jitter.ExtPacket{pkt}, 0, true
}

func (s *rtpClockPTS) GetPTS(pkt jitter.ExtPacket) (time.Duration, error) {
	if !s.hasFirst {
		s.first, s.hasFirst = pkt.Timestamp, true
	}
	return time.Duration(pkt.Timestamp-s.first) * time.Second / blankFrameRTPClockRate, nil
}

func (s *rtpClockPTS) OnSenderReport(func(time.Duration)) {}
func (s *rtpClockPTS) LastPTSAdjusted() time.Duration     { return 0 }
func (s *rtpClockPTS) Close()                             {}

// encodeRTPFrames encodes a short 320x240 test pattern and returns its RTP packets grouped by frame.
func encodeRTPFrames(t *testing.T, encodeAndPay string) [][]*rtp.Packet {
	t.Helper()

	pipeline, err := gst.NewPipelineFromString(
		"videotestsrc num-buffers=30 ! video/x-raw,width=320,height=240,framerate=30/1 ! " +
			encodeAndPay + " ! appsink name=sink sync=false",
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pipeline.SetState(gst.StateNull) })
	sinkElement, err := pipeline.GetElementByName("sink")
	require.NoError(t, err)
	sink := app.SinkFromElement(sinkElement)
	require.NoError(t, pipeline.SetState(gst.StatePlaying))

	var frames [][]*rtp.Packet
	for {
		sample := sink.PullSample()
		if sample == nil {
			break
		}
		pkt := &rtp.Packet{}
		require.NoError(t, pkt.Unmarshal(sample.GetBuffer().Bytes()))
		if n := len(frames); n == 0 || frames[n-1][0].Timestamp != pkt.Timestamp {
			frames = append(frames, nil)
		}
		frames[len(frames)-1] = append(frames[len(frames)-1], pkt)
	}
	require.NotEmpty(t, frames, "encoder produced no RTP")
	return frames
}

// sfuBlankFrames continues the stream with the blank keyframes the SFU writes when it closes a downtrack.
func sfuBlankFrames(last *rtp.Packet, payload func(*rtp.Packet) []byte) [][]*rtp.Packet {
	frames := make([][]*rtp.Packet, 0, sfuBlankFrameCount)
	header := last.Header
	for range sfuBlankFrameCount {
		header.SequenceNumber++
		header.Timestamp += blankFrameRTPClockRate / blankFrameFPS
		header.Marker = true
		pkt := &rtp.Packet{Header: header}
		pkt.Payload = payload(pkt)
		frames = append(frames, []*rtp.Packet{pkt})
	}
	return frames
}

// h264BlankPayload packs the SFU's 2x2 SPS, PPS and IDR into one STAP-A, as the SFU does.
func h264BlankPayload(*rtp.Packet) []byte {
	payload := []byte{0x18}
	for _, nal := range sfu.H264KeyFrame2x2 {
		payload = binary.BigEndian.AppendUint16(payload, uint16(len(nal)))
		payload = append(payload, nal...)
	}
	return payload
}

// vp8BlankPayload prefixes the SFU's 8x8 keyframe with a descriptor continuing the stream's picture ID.
func vp8BlankPayload(t *testing.T, last *rtp.Packet) func(*rtp.Packet) []byte {
	t.Helper()
	var desc codec.VP8
	require.NoError(t, desc.Unmarshal(last.Payload))
	pictureID := desc.PictureID
	return func(*rtp.Packet) []byte {
		pictureID = (pictureID + 1) & 0x7fff
		// X and S, then I, then a 15-bit picture ID
		payload := []byte{0x90, 0x80, 0x80 | byte(pictureID>>8), byte(pictureID)}
		return append(payload, sfu.VP8KeyFrame8x8...)
	}
}

// TestPassthroughSFUBlankFrames feeds a passthrough video track, followed by the SFU's
// end-of-downtrack blank keyframes, through pushSamples into the depay, parse and mux
// chain passthrough egress builds. mp4mux and webmmux refuse video under 16x16, so the
// blank frames must not reach them.
func TestPassthroughSFUBlankFrames(t *testing.T) {
	gstInitOnce.Do(func() { gst.Init(nil) })

	cases := []struct {
		name         string
		mimeType     types.MimeType
		encodeAndPay string
		receive      string
		blank        func(t *testing.T, last *rtp.Packet) func(*rtp.Packet) []byte
	}{
		{
			name:         "H264/MP4",
			mimeType:     types.MimeTypeH264,
			encodeAndPay: "x264enc tune=zerolatency key-int-max=30 ! video/x-h264,profile=constrained-baseline ! rtph264pay pt=96 config-interval=-1 mtu=1200",
			receive:      "rtph264depay ! video/x-h264,stream-format=byte-stream ! h264parse ! mp4mux",
			blank:        func(*testing.T, *rtp.Packet) func(*rtp.Packet) []byte { return h264BlankPayload },
		},
		{
			name:         "VP8/WebM",
			mimeType:     types.MimeTypeVP8,
			encodeAndPay: "vp8enc deadline=1 keyframe-max-dist=30 ! rtpvp8pay pt=96 picture-id-mode=15-bit mtu=1200",
			receive:      "rtpvp8depay ! webmmux",
			blank:        vp8BlankPayload,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames := encodeRTPFrames(t, tc.encodeAndPay)
			last := frames[len(frames)-1]
			frames = append(frames, sfuBlankFrames(last[len(last)-1], tc.blank(t, last[len(last)-1]))...)

			encodingName := map[types.MimeType]string{types.MimeTypeH264: "H264", types.MimeTypeVP8: "VP8"}[tc.mimeType]
			pipeline, err := gst.NewPipelineFromString(
				"appsrc name=src format=time is-live=true caps=application/x-rtp,media=video,payload=96,encoding-name=" +
					encodingName + ",clock-rate=90000 ! " + tc.receive + " ! fakesink",
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = pipeline.SetState(gst.StateNull) })
			srcElement, err := pipeline.GetElementByName("src")
			require.NoError(t, err)
			require.NoError(t, pipeline.SetState(gst.StatePlaying))

			callbacks := &gstreamer.Callbacks{}
			callbacks.SetOnError(func(err error) { t.Errorf("writer reported error: %v", err) })
			callbacks.OnPipelinePaused()

			translator := NewNullTranslator()
			if tc.mimeType == types.MimeTypeVP8 {
				translator = NewVP8Translator(logger.GetLogger())
			}
			w := &AppWriter{
				conf:         &config.PipelineConfig{Live: true},
				logger:       logger.GetLogger(),
				track:        &webrtc.TrackRemote{},
				codec:        tc.mimeType,
				src:          app.SrcFromElement(srcElement),
				callbacks:    callbacks,
				translator:   translator,
				trackSync:    &rtpClockPTS{},
				sendPLI:      func() {},
				timeProvider: gstreamer.NopTimeProvider(),
			}
			w.samplesCond = sync.NewCond(&w.samplesLock)
			w.Playing()
			go w.pushSamples()

			for _, frame := range frames {
				sample := make([]jitter.ExtPacket, 0, len(frame))
				for _, pkt := range frame {
					sample = append(sample, jitter.ExtPacket{ReceivedAt: time.Now(), Packet: pkt})
				}
				w.onPacket(sample)
			}
			w.endStreamSourceProcessed.Break()
			w.notifyPushSamples()
			select {
			case <-w.endStreamProcessed.Watch():
			case <-time.After(5 * time.Second):
				t.Fatal("pushSamples did not finish")
			}
			require.Equal(t, gst.FlowOK, w.src.EndStream())

			msg := pipeline.GetPipelineBus().TimedPopFiltered(gst.ClockTime(10*time.Second), gst.MessageEOS|gst.MessageError)
			require.NotNil(t, msg, "pipeline neither finished nor failed")
			if msg.Type() == gst.MessageError {
				gErr := msg.ParseError()
				t.Fatalf("passthrough chain failed: %s (%s)", gErr.Error(), gErr.DebugString())
			}
			require.Equal(t, uint64(sfuBlankFrameCount), w.stats.packetsDropped.Load(),
				"only the SFU blank frames should be dropped")
		})
	}
}
