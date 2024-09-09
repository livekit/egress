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
	lastRead  time.Time
	active    atomic.Bool
	playing   core.Fuse
	draining  core.Fuse
	endStream core.Fuse
	finished  core.Fuse
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
		logger.Infow("logging to file", "filename", logFilename)
		f, err := os.Create(logFilename)
		if err != nil {
			return nil, err
		}
		_, err = f.WriteString("pts,received_sn,adjusted_sn,received_ts,adjusted_ts\n")
		if err != nil {
			return nil, err
		}
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

	go w.start()
	return w, nil
}

func (w *AppWriter) start() {
	w.startTime = time.Now()
	w.active.Store(true)

	for !w.endStream.IsBroken() {
		w.readNext()
	}

	// clean up
	if w.playing.IsBroken() {
		w.callbacks.OnEOSSent()
		if flow := w.src.EndStream(); flow != gst.FlowOK && flow != gst.FlowFlushing {
			w.logger.Errorw("unexpected flow return", nil, "flowReturn", flow.String())
		}
	}

	w.draining.Break()

	stats := w.GetTrackStats()
	loss := w.buffer.PacketLoss()
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

func (w *AppWriter) readNext() {
	_ = w.track.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
	pkt, _, err := w.track.ReadRTP()
	if err != nil {
		var netErr net.Error
		switch {
		case w.draining.IsBroken():
			w.endStream.Break()
		case errors.As(err, &netErr) && netErr.Timeout():
			if !w.active.Load() {
				return
			}
			timeout := w.lastRead
			if timeout.IsZero() {
				timeout = w.startTime
			}
			if w.pub.IsMuted() || time.Since(timeout) > latency {
				// set track inactive
				w.logger.Debugw("track inactive", "timestamp", time.Since(w.startTime))
				w.active.Store(false)
				w.callbacks.OnTrackMuted(w.track.ID())
			}
		case err.Error() == errBufferTooSmall:
			w.logger.Warnw("read error", err)
		default:
			if !errors.Is(err, io.EOF) {
				w.logger.Errorw("could not read packet", err)
			}
			w.endStream.Break()
		}
		return
	}

	// initialize on first packet
	if w.lastRead.IsZero() {
		w.Initialize(pkt)
	}
	w.lastRead = time.Now()
	if !w.active.Swap(true) {
		// set track active
		w.logger.Debugw("track active", "timestamp", time.Since(w.startTime))
		w.callbacks.OnTrackUnmuted(w.track.ID())
		if w.sendPLI != nil {
			w.sendPLI()
		}
	}

	// push packet to jitter buffer
	w.buffer.Push(pkt)

	// buffers can only be pushed to the appsrc while in the playing state
	if !w.playing.IsBroken() {
		return
	}

	// push completed packets to appsrc
	if err = w.pushSamples(); err != nil {
		w.draining.Once(w.endStream.Break)
	}
}

func (w *AppWriter) pushSamples() error {
	pkts := w.buffer.Pop(false)
	for _, pkt := range pkts {
		sn := pkt.SequenceNumber
		ts := pkt.Timestamp

		w.translator.Translate(pkt)

		// get PTS
		pts, err := w.GetPTS(pkt)
		if err != nil {
			if errors.Is(err, synchronizer.ErrBackwardsPTS) {
				return nil
			}
			return err
		}

		if w.logFile != nil {
			_, _ = w.logFile.WriteString(fmt.Sprintf("%s,%d,%d,%d,%d\n",
				pts.String(),
				sn, pkt.SequenceNumber,
				ts, pkt.Timestamp,
			))
		}

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
	}

	return nil
}

func (w *AppWriter) Playing() {
	w.playing.Break()
}

// Drain blocks until finished
func (w *AppWriter) Drain(force bool) {
	w.draining.Once(func() {
		w.logger.Debugw("draining")

		if force || !w.active.Load() {
			w.endStream.Break()
		} else {
			time.AfterFunc(drainTimeout, w.endStream.Break)
		}
	})

	// wait until finished
	<-w.finished.Watch()
}
