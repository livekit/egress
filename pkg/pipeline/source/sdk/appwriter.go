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
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/server-sdk-go/v2/pkg/jitter"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"
)

type state int

const (
	statePlaying state = iota
	stateMuted
	stateUnmuting
	stateReconnecting
)

const (
	latency           = time.Second * 2
	drainTimeout      = time.Second * 4
	errBufferTooSmall = "buffer too small"
)

type AppWriter struct {
	logger    logger.Logger
	logFile   *os.File
	pub       lksdk.TrackPublication
	track     *webrtc.TrackRemote
	codec     types.MimeType
	src       *app.Source
	startTime time.Time

	buffer      *jitter.Buffer
	translator  Translator
	callbacks   *gstreamer.Callbacks
	sendPLI     func()
	pliThrottle core.Throttle

	// a/v sync
	sync *synchronizer.Synchronizer
	*synchronizer.TrackSynchronizer

	// state
	state        state
	initialized  bool
	ticker       *time.Ticker
	muted        atomic.Bool
	disconnected atomic.Bool
	playing      core.Fuse
	draining     core.Fuse
	endStream    core.Fuse
	finished     core.Fuse
}

func NewAppWriter(
	track *webrtc.TrackRemote,
	pub lksdk.TrackPublication,
	rp *lksdk.RemoteParticipant,
	ts *config.TrackSource,
	sync *synchronizer.Synchronizer,
	callbacks *gstreamer.Callbacks,
	logFilename string,
) (*AppWriter, error) {
	w := &AppWriter{
		logger:            logger.GetLogger().WithValues("trackID", track.ID(), "kind", track.Kind().String()),
		track:             track,
		pub:               pub,
		codec:             ts.MimeType,
		src:               ts.AppSrc,
		callbacks:         callbacks,
		sync:              sync,
		TrackSynchronizer: sync.AddTrack(track, rp.Identity()),
	}

	if logFilename != "" {
		f, err := os.Create(logFilename)
		if err != nil {
			return nil, err
		}
		_, _ = f.WriteString("pts,sn,ts\n")
		w.logFile = f
	}

	var depacketizer rtp.Depacketizer
	switch ts.MimeType {
	case types.MimeTypeOpus:
		depacketizer = &codecs.OpusPacket{}
		w.translator = NewNullTranslator()

	case types.MimeTypeH264:
		depacketizer = &codecs.H264Packet{}
		w.translator = NewNullTranslator()

	case types.MimeTypeVP8:
		depacketizer = &codecs.VP8Packet{}
		w.translator = NewVP8Translator(w.logger)

	case types.MimeTypeVP9:
		depacketizer = &codecs.VP9Packet{}
		w.translator = NewNullTranslator()

	default:
		return nil, errors.ErrNotSupported(string(ts.MimeType))
	}

	if track.Kind() == webrtc.RTPCodecTypeVideo {
		w.pliThrottle = core.NewThrottle(time.Second)
		w.sendPLI = func() { w.pliThrottle(func() { rp.WritePLI(track.SSRC()) }) }
	}

	w.buffer = jitter.NewBuffer(
		depacketizer,
		ts.ClockRate,
		latency,
		jitter.WithPacketDroppedHandler(w.sendPLI),
		jitter.WithLogger(w.logger),
	)

	go w.run()
	return w, nil
}

func (w *AppWriter) TrackID() string {
	return w.track.ID()
}

func (w *AppWriter) Play() {
	w.playing.Break()
	if w.pub.IsMuted() || w.disconnected.Load() {
		w.SetTrackMuted(true)
	}
}

func (w *AppWriter) SetTrackMuted(muted bool) {
	if !w.playing.IsBroken() || w.muted.Load() == muted {
		return
	}

	w.muted.Store(muted)
	if muted {
		w.logger.Debugw("track muted", "timestamp", time.Since(w.startTime).Seconds())
		w.callbacks.OnTrackMuted(w.track.ID())
	} else {
		w.logger.Debugw("track unmuted", "timestamp", time.Since(w.startTime).Seconds())
		if w.sendPLI != nil {
			w.sendPLI()
		}
	}
}

func (w *AppWriter) SetTrackDisconnected(disconnected bool) {
	w.disconnected.Store(disconnected)
	if disconnected {
		w.logger.Debugw("track disconnected", "timestamp", time.Since(w.startTime).Seconds())
		if w.playing.IsBroken() {
			w.callbacks.OnTrackMuted(w.track.ID())
		}
	} else {
		w.logger.Debugw("track reconnected", "timestamp", time.Since(w.startTime).Seconds())
		if w.playing.IsBroken() && !w.muted.Load() && w.sendPLI != nil {
			w.sendPLI()
		}
	}
}

// Drain blocks until finished
func (w *AppWriter) Drain(force bool) {
	w.draining.Once(func() {
		w.logger.Debugw("draining")

		if force || w.muted.Load() || w.disconnected.Load() {
			w.endStream.Break()
		} else {
			// wait until drainTimeout before force popping
			time.AfterFunc(drainTimeout, w.endStream.Break)
		}
	})

	// wait until finished
	<-w.finished.Watch()
}

func (w *AppWriter) run() {
	w.startTime = time.Now()

	for !w.endStream.IsBroken() {
		switch w.state {
		case statePlaying, stateReconnecting, stateUnmuting:
			w.handlePlaying()
		case stateMuted:
			w.handleMuted()
		}
	}

	// clean up
	_ = w.pushSamples()
	if w.playing.IsBroken() {
		if flow := w.src.EndStream(); flow != gst.FlowOK && flow != gst.FlowFlushing {
			w.logger.Errorw("unexpected flow return", nil, "flowReturn", flow.String())
		}
	}

	stats := w.GetTrackStats()
	loss := w.buffer.PacketLoss()

	w.draining.Break()
	w.logger.Infow("writer finished",
		"sampleDuration", fmt.Sprint(w.GetFrameDuration()),
		"avgDrift", fmt.Sprint(time.Duration(stats.AvgDrift)),
		"maxDrift", fmt.Sprint(stats.MaxDrift),
		"packetLoss", fmt.Sprintf("%.2f%%", loss*100),
	)

	if w.logFile != nil {
		_ = w.logFile.Close()
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

	// push packet to jitter buffer
	w.buffer.Push(pkt)

	// push completed packets to appsrc
	if err = w.pushSamples(); err != nil {
		w.draining.Once(w.endStream.Break)
	}
}

func (w *AppWriter) handleMuted() {
	switch {
	case w.draining.IsBroken():
		w.ticker.Stop()
		w.endStream.Break()

	case !w.muted.Load():
		w.ticker.Stop()
		w.state = stateUnmuting

	default:
		<-w.ticker.C
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

	// check if reconnecting
	if w.disconnected.Load() {
		_ = w.pushSamples()
		w.state = stateReconnecting
		return
	}

	// check if muted
	if w.muted.Load() {
		_ = w.pushSamples()
		w.ticker = time.NewTicker(w.GetFrameDuration())
		w.state = stateMuted
		return
	}

	// continue on timeout
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return
	}

	// log non-EOF errors
	if !errors.Is(err, io.EOF) {
		w.logger.Errorw("could not read packet", err)
	}

	// end stream
	w.endStream.Break()
}

func (w *AppWriter) pushSamples() error {
	// buffers can only be pushed to the appsrc while in the playing state
	if !w.playing.IsBroken() {
		return nil
	}

	pkts := w.buffer.Pop(false)
	for _, pkt := range pkts {
		w.translator.Translate(pkt)

		// get PTS
		pts, err := w.GetPTS(pkt)
		if err != nil {
			if errors.Is(err, synchronizer.ErrBackwardsPTS) {
				return nil
			}
			return err
		}

		if w.state == stateUnmuting || w.state == stateReconnecting {
			w.callbacks.OnTrackUnmuted(w.track.ID(), pts)
			w.state = statePlaying
		}

		if err = w.pushPacket(pkt, pts); err != nil {
			return err
		}
	}

	return nil
}

func (w *AppWriter) pushPacket(pkt *rtp.Packet, pts time.Duration) error {
	p, err := pkt.Marshal()
	if err != nil {
		w.logger.Errorw("could not marshal packet", err)
		return err
	}

	b := gst.NewBufferFromBytes(p)
	b.SetPresentationTimestamp(gst.ClockTime(uint64(pts)))
	if flow := w.src.PushBuffer(b); flow != gst.FlowOK {
		w.logger.Infow("unexpected flow return", "flow", flow)
	}

	if w.logFile != nil {
		_, _ = w.logFile.WriteString(fmt.Sprintf("%s,%d,%d\n", pts.String(), pkt.SequenceNumber, pkt.Timestamp))
	}

	return nil
}
