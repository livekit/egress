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
	"io"
	"net"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/logging"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/media-sdk/jitter"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"
)

const (
	errBufferTooSmall      = "buffer too small"
	discontinuityTolerance = 500 * time.Millisecond
)

type AppWriter struct {
	conf *config.PipelineConfig

	logger    logger.Logger
	csvLogger *logging.CSVLogger[logging.TrackStats]
	drift     atomic.Duration
	maxDrift  atomic.Duration

	pub       lksdk.TrackPublication
	track     *webrtc.TrackRemote
	codec     types.MimeType
	src       *app.Source
	startTime time.Time

	buffer      *jitter.Buffer
	samples     chan []*rtp.Packet
	translator  Translator
	callbacks   *gstreamer.Callbacks
	sendPLI     func()
	pliThrottle core.Throttle

	// a/v sync
	sync *synchronizer.Synchronizer
	*synchronizer.TrackSynchronizer
	driftHandler DriftHandler

	lastPTS   time.Duration
	lastDrift time.Duration

	// state
	buildReady   core.Fuse
	active       atomic.Bool
	lastReceived atomic.Time
	lastPushed   atomic.Time
	playing      core.Fuse
	draining     core.Fuse
	endStream    core.Fuse
	finished     core.Fuse
	stats        appWriterStats
}

type appWriterStats struct {
	packetsDropped atomic.Uint64
}

type DriftHandler interface {
	EnqueueDrift(t time.Duration)
	Processed() time.Duration
}

func NewAppWriter(
	conf *config.PipelineConfig,
	track *webrtc.TrackRemote,
	pub lksdk.TrackPublication,
	rp *lksdk.RemoteParticipant,
	ts *config.TrackSource,
	sync *synchronizer.Synchronizer,
	driftHandler DriftHandler,
	callbacks *gstreamer.Callbacks,
) (*AppWriter, error) {
	w := &AppWriter{
		conf:              conf,
		logger:            logger.GetLogger().WithValues("trackID", track.ID(), "kind", track.Kind().String()),
		track:             track,
		pub:               pub,
		codec:             ts.MimeType,
		src:               ts.AppSrc,
		samples:           make(chan []*rtp.Packet, 100),
		callbacks:         callbacks,
		sync:              sync,
		TrackSynchronizer: sync.AddTrack(track, rp.Identity()),
		driftHandler:      driftHandler,
	}

	if conf.Debug.EnableTrackLogging {
		csvLogger, err := logging.NewCSVLogger[logging.TrackStats](track.ID())
		if err != nil {
			logger.Errorw("failed to create csv logger", err)
		} else {
			w.csvLogger = csvLogger
			w.TrackSynchronizer.OnSenderReport(func(drift time.Duration) {
				logger.Debugw("received sender report", "drift", drift)
				if w.driftHandler != nil {
					// presence of the drift handler means that PTS updates on SRs are disabled
					d := drift - w.lastDrift
					w.lastDrift = drift
					w.driftHandler.EnqueueDrift(d)
				}
				w.updateDrift(drift)
			})
		}
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
		conf.Latency.JitterBufferLatency,
		w.onPacket,
		jitter.WithLogger(w.logger),
		jitter.WithPacketLossHandler(w.sendPLI),
	)

	go w.start()
	return w, nil
}

func (w *AppWriter) start() {
	w.startTime = time.Now()
	w.active.Store(true)
	if w.csvLogger != nil {
		go w.logStats()
	}

	go func() {
		<-w.callbacks.BuildReady
		w.buildReady.Once(func() {
			if !w.active.Load() {
				w.callbacks.OnTrackMuted(w.track.ID())
			}
		})
	}()

	go w.pushSamples()
	for !w.endStream.IsBroken() {
		w.readNext()
	}

	// clean up
	if w.playing.IsBroken() {
		w.callbacks.OnEOSSent()
		if flow := w.src.EndStream(); flow != gst.FlowOK && flow != gst.FlowFlushing {
			w.logger.Errorw("unexpected flow return", nil, "flowReturn", flow.String())
		}
		if w.driftHandler != nil {
			w.logger.Debugw("processed drift", "drift", w.driftHandler.Processed())
		}
	}

	w.logger.Infow("writer finished")
	if w.csvLogger != nil {
		w.csvLogger.Close()
	}

	w.finished.Break()
}

func (w *AppWriter) readNext() {
	_ = w.track.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
	pkt, _, err := w.track.ReadRTP()
	if err != nil {
		w.handleReadError(err)
		return
	}

	// initialize on first packet
	if w.lastReceived.Load().IsZero() {
		w.Initialize(pkt)
	}
	w.lastReceived.Store(time.Now())

	if !w.active.Swap(true) {
		// set track active
		w.logger.Debugw("track active", "timestamp", time.Since(w.startTime))
		if w.buildReady.IsBroken() {
			w.callbacks.OnTrackUnmuted(w.track.ID())
		}
		if w.sendPLI != nil {
			w.sendPLI()
		}
	}

	// push packet to jitter buffer
	w.buffer.Push(pkt)
}

func (w *AppWriter) handleReadError(err error) {
	var netErr net.Error
	switch {
	case w.draining.IsBroken():
		w.endStream.Break()

	case errors.As(err, &netErr) && netErr.Timeout():
		if !w.active.Load() {
			return
		}
		lastRecv := w.lastReceived.Load()
		if lastRecv.IsZero() {
			lastRecv = w.startTime
		}
		if w.pub.IsMuted() || time.Since(lastRecv) > w.conf.Latency.JitterBufferLatency {
			// set track inactive
			w.logger.Debugw("track inactive", "timestamp", time.Since(w.startTime))
			w.active.Store(false)
			if w.buildReady.IsBroken() {
				w.callbacks.OnTrackMuted(w.track.ID())
			}
		}

	case err.Error() == errBufferTooSmall:
		w.logger.Warnw("read error", err)

	default:
		if !errors.Is(err, io.EOF) {
			w.logger.Errorw("could not read packet", err)
		}
		w.draining.Break()
		w.endStream.Break()
	}
}

func (w *AppWriter) onPacket(sample []*rtp.Packet) {
	select {
	case w.samples <- sample:
		// ok
	default:
		w.stats.packetsDropped.Add(uint64(len(sample)))
		w.logger.Warnw("buffer full, dropping sample", nil)
	}

}

func (w *AppWriter) pushSamples() {
	select {
	case <-w.playing.Watch():
		// continue
	case <-w.draining.Watch():
		return
	}

	end := w.endStream.Watch()
	for {
		select {
		case <-end:
			return
		case sample := <-w.samples:
			for _, pkt := range sample {
				if err := w.pushPacket(pkt); err != nil {
					w.draining.Break()
					w.endStream.Break()
				}
			}
		}
	}
}

func (w *AppWriter) pushPacket(pkt *rtp.Packet) error {
	w.translator.Translate(pkt)

	// get PTS
	pts, err := w.GetPTS(pkt)
	if err != nil {
		w.stats.packetsDropped.Inc()
		return err
	}

	p, err := pkt.Marshal()
	if err != nil {
		w.stats.packetsDropped.Inc()
		w.logger.Errorw("could not marshal packet", err)
		return err
	}

	b := gst.NewBufferFromBytes(p)
	b.SetPresentationTimestamp(gst.ClockTime(uint64(pts)))

	if isDiscontinuity(w.lastPTS, pts) {
		if w.shouldHandleDiscontinuity() {
			w.logger.Debugw("discontinuity detected", "pts", pts, "lastPTS", w.lastPTS)
			ok := w.src.SendEvent(gst.NewFlushStartEvent())
			if !ok {
				w.logger.Errorw("failed to send flush start event", nil)
			}
			ok = w.src.SendEvent(gst.NewFlushStopEvent(false))
			if !ok {
				w.logger.Errorw("failed to send flush stop event", nil)
			}
		}
		b.SetFlags(b.GetFlags() | gst.BufferFlagDiscont)
	}

	if flow := w.src.PushBuffer(b); flow != gst.FlowOK {
		w.stats.packetsDropped.Inc()
		w.logger.Infow("unexpected flow return", "flow", flow)
	}
	w.lastPushed.Store(time.Now())
	w.lastPTS = pts
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
			time.AfterFunc(w.conf.Latency.PipelineLatency, func() { w.endStream.Break() })
		}
	})

	// wait until finished
	<-w.finished.Watch()
}

func (w *AppWriter) logStats() {
	ended := w.endStream.Watch()
	ticker := time.NewTicker(time.Second * 10)
	defer ticker.Stop()

	for {
		select {
		case <-ended:
			stats := w.getStats()
			w.csvLogger.Write(stats)
			w.csvLogger.Close()
			w.logger.Debugw("appwriter stats ", "stats", stats)
			return

		case <-ticker.C:
			stats := w.getStats()
			w.csvLogger.Write(stats)
		}
	}
}

func (w *AppWriter) getStats() *logging.TrackStats {
	stats := w.buffer.Stats()
	return &logging.TrackStats{
		Timestamp:       time.Now().Format(time.DateTime),
		PacketsReceived: stats.PacketsPushed,
		PaddingReceived: stats.PaddingPushed,
		LastReceived:    w.lastReceived.Load().Format(time.DateTime),
		PacketsDropped:  stats.PacketsDropped + w.stats.packetsDropped.Load(),
		PacketsPushed:   stats.PacketsPopped,
		SamplesPushed:   stats.SamplesPopped,
		LastPushed:      w.lastPushed.Load().Format(time.DateTime),
		Drift:           w.drift.Load(),
		MaxDrift:        w.maxDrift.Load(),
	}
}

func (w *AppWriter) updateDrift(drift time.Duration) {
	w.drift.Store(drift)
	for {
		maxDrift := w.maxDrift.Load()
		if drift.Abs() <= maxDrift.Abs() {
			break
		}
		if w.maxDrift.CompareAndSwap(maxDrift, drift) {
			break
		}
	}
}

func (w *AppWriter) shouldHandleDiscontinuity() bool {
	return w.track.Kind() == webrtc.RTPCodecTypeAudio && w.conf.AudioTempoController.Enabled
}

func isDiscontinuity(lastPTS time.Duration, pts time.Duration) bool {
	return pts > lastPTS+discontinuityTolerance
}
