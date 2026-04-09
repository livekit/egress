// Copyright 2024 LiveKit, Inc.
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

// Package testfeeder provides a non-live media file feeder for GStreamer appsrc
// elements. It reads encoded media from files (H.264 annexb, VP8/VP9 IVF, Opus
// OGG), packetizes the frames into RTP packets, and pushes them to an appsrc as
// fast as the pipeline will accept. This is used to test the egress GStreamer
// pipeline in non-live (faster-than-realtime) mode without needing a live
// WebRTC source.
package testfeeder

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
)

const (
	defaultMTU       = 1200
	videoClockRate   = 90000
	opusClockRate    = 48000
	defaultVideoFPS  = 30
	defaultOpusFrame = 20 * time.Millisecond
)

// FrameReader reads encoded media frames from a file.
// Each call to NextFrame returns the raw encoded data for one frame
// and the frame's duration in RTP clock rate units (samples).
type FrameReader interface {
	// NextFrame returns the next frame's payload and its duration in RTP
	// timestamp units (samples at the codec clock rate). Returns io.EOF
	// when all frames have been read.
	NextFrame() (payload []byte, samples uint32, err error)

	// ClockRate returns the RTP clock rate for this media type.
	ClockRate() uint32

	io.Closer
}

// TrackFeeder reads frames from a FrameReader, packetizes them into RTP, and
// pushes the resulting buffers to a GStreamer appsrc element. When the appsrc
// is configured with is-live=false, PushBuffer blocks on backpressure,
// naturally throttling the feeder to the pipeline's processing speed.
type TrackFeeder struct {
	src        *app.Source
	reader     FrameReader
	packetizer rtp.Packetizer
	codec      types.MimeType
	startTS    uint32
	currentTS  uint32
	ptsOffset  time.Duration // accumulated offset from GAP events
}

// TrackFeederParams configures a TrackFeeder.
type TrackFeederParams struct {
	// AppSrc is the GStreamer appsrc element to push buffers to.
	AppSrc *app.Source

	// MimeType selects the codec (types.MimeTypeH264, MimeTypeVP8, MimeTypeOpus, etc.).
	MimeType types.MimeType

	// PayloadType is the RTP payload type number (e.g. 96 for H264, 111 for Opus).
	PayloadType uint8

	// SSRC for the RTP stream. Use any fixed value for testing.
	SSRC uint32

	// Reader is the frame source. Use NewH264Reader, NewIVFReader, or NewOGGReader.
	Reader FrameReader
}

// NewTrackFeeder creates a TrackFeeder that reads from the given FrameReader
// and pushes RTP-packetized buffers to the appsrc.
func NewTrackFeeder(params TrackFeederParams) (*TrackFeeder, error) {
	payloader, err := payloaderForCodec(params.MimeType)
	if err != nil {
		return nil, err
	}

	clockRate := params.Reader.ClockRate()

	packetizer := rtp.NewPacketizer(
		defaultMTU,
		params.PayloadType,
		params.SSRC,
		payloader,
		rtp.NewRandomSequencer(),
		clockRate,
	)

	return &TrackFeeder{
		src:        params.AppSrc,
		reader:     params.Reader,
		packetizer: packetizer,
		codec:      params.MimeType,
	}, nil
}

// Feed reads all frames from the reader, packetizes them, and pushes them to
// the appsrc. It blocks until all frames are consumed (io.EOF from reader) or
// an error occurs. On completion it sends an EOS event to the appsrc.
//
// With is-live=false on the appsrc, PushBuffer provides natural backpressure:
// when the downstream pipeline is busy, the push blocks, so no rate-limiting
// code is needed.
func (f *TrackFeeder) Feed() error {
	defer f.sendEOS()

	// Log appsrc properties
	if maxBuf, err := f.src.GetProperty("max-buffers"); err == nil {
		logger.Infow("appsrc config",
			"max-buffers", maxBuf,
			"is-live", func() interface{} { v, _ := f.src.GetProperty("is-live"); return v }(),
			"max-bytes", func() interface{} { v, _ := f.src.GetProperty("max-bytes"); return v }(),
			"current-level-buffers", func() interface{} { v, _ := f.src.GetProperty("current-level-buffers"); return v }(),
		)
	}

	clockRate := f.reader.ClockRate()
	firstFrame := true
	frameCount := 0
	packetCount := 0
	totalBytes := 0
	start := time.Now()

	for {
		payload, samples, err := f.reader.NextFrame()
		if err == io.EOF {
			logger.Infow("feeder done",
				"frames", frameCount,
				"packets", packetCount,
				"bytes", totalBytes,
				"elapsed", time.Since(start),
				"codec", f.codec,
			)
			return nil
		}
		var gapErr *GapError
		if errors.As(err, &gapErr) {
			pts := rtpToDuration(f.currentTS, clockRate)
			logger.Infow("feeder sending GAP event",
				"pts", pts,
				"gapDuration", gapErr.Duration,
				"framesBeforeGap", frameCount,
			)
			if err := f.sendGapEvent(pts+f.ptsOffset, gapErr.Duration); err != nil {
				return fmt.Errorf("sending GAP event: %w", err)
			}
			// Accumulate the gap so subsequent buffer PTS values are shifted forward.
			f.ptsOffset += gapErr.Duration
			continue
		}
		if err != nil {
			return fmt.Errorf("reading frame: %w", err)
		}

		frameCount++
		packets := f.packetizer.Packetize(payload, samples)
		for _, pkt := range packets {
			if firstFrame {
				// Capture the actual RTP timestamp from the first packet.
				// The packetizer starts with a random base timestamp.
				f.startTS = pkt.Timestamp
				firstFrame = false
			}
			packetCount++
			if err := f.pushRTPPacket(pkt, clockRate); err != nil {
				return fmt.Errorf("pushing packet seq=%d: %w", pkt.SequenceNumber, err)
			}
			totalBytes += len(pkt.Payload)
		}

		f.currentTS += samples

		if frameCount%100 == 0 {
			pts := rtpToDuration(f.currentTS, clockRate)
			logger.Debugw("feeder progress",
				"frames", frameCount,
				"packets", packetCount,
				"mediaPTS", pts,
				"elapsed", time.Since(start),
			)
		}
	}
}

// pushRTPPacket marshals an RTP packet, creates a GstBuffer with the
// appropriate PTS, and pushes it to the appsrc.
func (f *TrackFeeder) pushRTPPacket(pkt *rtp.Packet, clockRate uint32) error {
	buf, err := pkt.Marshal()
	if err != nil {
		return fmt.Errorf("marshaling RTP packet: %w", err)
	}

	pts := rtpToDuration(pkt.Timestamp-f.startTS, clockRate) + f.ptsOffset

	b := gst.NewBufferFromBytes(buf)
	b.SetPresentationTimestamp(gst.ClockTime(uint64(pts)))

	flow := f.src.PushBuffer(b)
	if flow != gst.FlowOK {
		return fmt.Errorf("appsrc push returned %v", flow)
	}
	return nil
}

func (f *TrackFeeder) sendEOS() {
	f.src.EndStream()
}

// sendGapEvent pushes a GAP event downstream from the appsrc, telling the
// mixer to insert silence for this track over the given duration.
//
// GAP is a downstream serialized event. We push it via Pad.PushEvent on the
// appsrc's src pad (not SendEvent, which is for upstream events).
func (f *TrackFeeder) sendGapEvent(pts time.Duration, duration time.Duration) error {
	event := gst.NewGapEvent(
		gst.ClockTime(uint64(pts)),
		gst.ClockTime(uint64(duration)),
	)
	srcPad := f.src.GetStaticPad("src")
	if srcPad == nil {
		return fmt.Errorf("appsrc has no src pad")
	}
	if !srcPad.PushEvent(event) {
		return fmt.Errorf("failed to push GAP event on appsrc src pad")
	}
	return nil
}

// rtpToDuration converts an RTP timestamp delta to a time.Duration.
func rtpToDuration(rtpTS uint32, clockRate uint32) time.Duration {
	return time.Duration(float64(rtpTS) / float64(clockRate) * float64(time.Second))
}

func payloaderForCodec(mime types.MimeType) (rtp.Payloader, error) {
	switch mime {
	case types.MimeTypeH264:
		return &codecs.H264Payloader{}, nil
	case types.MimeTypeVP8:
		return &codecs.VP8Payloader{
			EnablePictureID: true,
		}, nil
	case types.MimeTypeVP9:
		return &codecs.VP9Payloader{}, nil
	case types.MimeTypeOpus:
		return &codecs.OpusPayloader{}, nil
	default:
		return nil, fmt.Errorf("unsupported codec for test feeder: %s", mime)
	}
}
