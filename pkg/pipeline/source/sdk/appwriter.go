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
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/linkdata/deadlock"
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
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"
)

const (
	errBufferTooSmall      = "buffer too small"
	discontinuityTolerance = 500 * time.Millisecond
	pipelineCheckInterval  = 5 * time.Second
	cSamplesQueueDepth     = 100
	drainingTimeout        = time.Second * 3
)

type sampleItem struct {
	sample []jitter.ExtPacket
	next   *sampleItem
}

type AppWriter struct {
	conf *config.PipelineConfig

	logger    logger.Logger
	csvLogger *logging.CSVLogger[logging.TrackStats]
	drift     atomic.Duration
	maxDrift  atomic.Duration

	pub         lksdk.TrackPublication
	track       *webrtc.TrackRemote
	codec       types.MimeType
	src         *app.Source
	startTime   time.Time
	trackSource *config.TrackSource

	buffer *jitter.Buffer

	samplesHead *sampleItem
	samplesTail *sampleItem
	samplesLen  int
	samplesLock deadlock.Mutex
	samplesCond *sync.Cond

	translator  Translator
	callbacks   *gstreamer.Callbacks
	sendPLI     func()
	pliThrottle core.Throttle

	// a/v sync
	synchronizer *synchronizer.Synchronizer
	*synchronizer.TrackSynchronizer
	driftHandler DriftHandler

	lastPTS              time.Duration
	lastDrift            time.Duration
	lastPipelineCheckPTS time.Duration
	initialized          bool

	// state
	buildReady               core.Fuse
	active                   atomic.Bool
	lastReceived             atomic.Time
	lastPushed               atomic.Time
	playing                  core.Fuse
	draining                 core.Fuse
	endStreamSignaled        core.Fuse
	endStreamSourceProcessed core.Fuse
	endStreamProcessed       core.Fuse
	finished                 core.Fuse
	stats                    appWriterStats

	// diagnostics, set on unexpected flushing when pushing packets to the pipeline
	flushDotRequested atomic.Bool

	// ensure selector/bin removal is only triggered once on terminal read errors
	removalRequested atomic.Bool

	tpLock       deadlock.RWMutex
	timeProvider gstreamer.TimeProvider
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
	synchronizer *synchronizer.Synchronizer,
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
		trackSource:       ts,
		callbacks:         callbacks,
		synchronizer:      synchronizer,
		TrackSynchronizer: synchronizer.AddTrack(track, rp.Identity()),
		driftHandler:      driftHandler,
		timeProvider:      gstreamer.NopTimeProvider(),
	}
	w.samplesCond = sync.NewCond(&w.samplesLock)

	ts.OnKeyframeRequired = w.onKeyframeRequired

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

	case types.MimeTypePCMU, types.MimeTypePCMA:
		depacketizer = &G711Packet{}
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
	for !w.endStreamSignaled.IsBroken() {
		w.readNext()
	}
	w.drainJitterBuffer()

	select {
	case <-w.endStreamProcessed.Watch():
		w.logger.Debugw("endStreamProcessed fuse broken")
	case <-time.After(drainingTimeout):
		w.logger.Errorw("endStreamProcessed not broken after 3 seconds, bug in the draining logic!", nil,
			"endStreamSourceProcessed", w.endStreamSourceProcessed.IsBroken(),
			"playing", w.playing.IsBroken(),
			"active", w.active.Load(),
			"lastReceived", w.lastReceived.Load(),
			"lastPushed", w.lastPushed.Load(),
			"lastPTS", w.lastPTS,
		)
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

	if w.trackSource != nil {
		w.trackSource.OnKeyframeRequired = nil
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

	receivedAt := time.Now()
	var packets []jitter.ExtPacket
	if !w.initialized {
		ready, dropped, done := w.PrimeForStart(jitter.ExtPacket{ReceivedAt: receivedAt, Packet: pkt})
		if dropped > 0 {
			w.stats.packetsDropped.Add(uint64(dropped))
			if w.sendPLI != nil {
				w.sendPLI()
			}
		}
		if !done {
			return
		}
		w.initialized = true
		packets = ready
		w.lastReceived.Store(ready[len(ready)-1].ReceivedAt)
	} else {
		w.lastReceived.Store(receivedAt)
	}

	if !w.active.Swap(true) {
		// set track active
		w.logTrackState("track active")
		if w.buildReady.IsBroken() {
			w.callbacks.OnTrackUnmuted(w.track.ID())
		}
		if w.sendPLI != nil {
			w.sendPLI()
		}
	}
	if len(packets) > 0 {
		w.buffer.PushExtPacketBatch(packets)
	} else {
		w.buffer.Push(pkt)
	}
}

func (w *AppWriter) handleReadError(err error) {
	var netErr net.Error
	switch {
	case w.draining.IsBroken():
		w.endStreamSignaled.Break()
		w.notifyPushSamples()

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
			w.logTrackState("track inactive")
			w.active.Store(false)
			if w.buildReady.IsBroken() {
				w.callbacks.OnTrackMuted(w.track.ID())
			}
		}

	case err.Error() == errBufferTooSmall:
		w.logger.Warnw("read error", err)

	default:
		// ensure selector switches before EOS propagation to avoid encoder errors
		w.ensureRemovedBeforeDrain()

		if !errors.Is(err, io.EOF) {
			w.logger.Errorw("could not read packet", err)
		} else {
			w.logger.Debugw("read EOF, signaling end of stream")
		}
		w.draining.Break()
		w.endStreamSignaled.Break()
		w.notifyPushSamples()
	}
}

func (w *AppWriter) SetTimeProvider(tp gstreamer.TimeProvider) {
	w.tpLock.Lock()
	if tp == nil {
		tp = gstreamer.NopTimeProvider()
	}
	w.timeProvider = tp
	w.tpLock.Unlock()
}

func (w *AppWriter) waitFor(ch <-chan struct{}) bool {
	if ch == nil {
		return true
	}
	select {
	case <-ch:
		return true
	case <-w.draining.Watch():
		return false
	}
}

func (w *AppWriter) pipelineRunningTime() (time.Duration, bool) {
	w.tpLock.RLock()
	provider := w.timeProvider
	w.tpLock.RUnlock()
	return provider.RunningTime()
}

func (w *AppWriter) pipelinePlayhead() (time.Duration, bool) {
	w.tpLock.RLock()
	provider := w.timeProvider
	w.tpLock.RUnlock()
	return provider.PlayheadPosition()
}

func (w *AppWriter) logTrackState(event string) {
	fields := []any{"timestamp", time.Since(w.startTime)}
	if pipelineTime, ok := w.pipelineRunningTime(); ok {
		fields = append(fields, "pipeline_time", pipelineTime)
	}
	if playhead, ok := w.pipelinePlayhead(); ok {
		fields = append(fields, "playhead", playhead)
	}
	w.logger.Debugw(event, fields...)
}

func (w *AppWriter) onKeyframeRequired() {
	if w.finished.IsBroken() || w.sendPLI == nil {
		return
	}
	w.sendPLI()
}

func (w *AppWriter) notifyPushSamples() {
	w.samplesLock.Lock()
	w.samplesCond.Broadcast()
	w.samplesLock.Unlock()
}

func (w *AppWriter) onPacket(sample []jitter.ExtPacket) {
	w.samplesLock.Lock()
	item := &sampleItem{sample, nil}
	if w.samplesHead == nil {
		w.samplesHead = item
		w.samplesTail = w.samplesHead
		w.samplesLen = 1
	} else {
		w.samplesTail.next = item
		w.samplesTail = item
		w.samplesLen++
	}
	// drop old samples if queue is overflowing
	for w.samplesLen > cSamplesQueueDepth {
		if w.samplesHead != nil {
			itemToDrop := w.samplesHead
			w.samplesHead = w.samplesHead.next
			w.samplesLen--
			w.stats.packetsDropped.Add(uint64(len(itemToDrop.sample)))
			w.logger.Warnw("buffer full, dropping sample", nil, "numPackets", len(itemToDrop.sample))
		}
		if w.samplesHead == nil {
			w.samplesTail = nil
			w.samplesLen = 0
		}
	}
	w.samplesCond.Broadcast()
	w.samplesLock.Unlock()
}

func (w *AppWriter) pushSamples() {
	defer func() {
		w.endStreamSignaled.Break()
		w.endStreamProcessed.Break()
		w.logger.Debugw("pushSamples finished")
	}()
	if !w.waitFor(w.callbacks.PipelinePaused()) {
		return
	}

	if !w.waitFor(w.playing.Watch()) {
		return
	}

	for {
		w.samplesLock.Lock()
		for w.samplesHead == nil && !w.endStreamSourceProcessed.IsBroken() {
			w.samplesCond.Wait()
		}
		if w.endStreamSourceProcessed.IsBroken() && w.samplesHead == nil {
			w.samplesLock.Unlock()
			return
		}

		item := w.samplesHead
		w.samplesHead = item.next
		w.samplesLen--
		if w.samplesHead == nil {
			w.samplesTail = nil
		}
		w.samplesLock.Unlock()

		for _, pkt := range item.sample {
			if err := w.pushPacket(pkt); err != nil {
				if !utils.ErrorIsOneOf(err, synchronizer.ErrPacketOutOfOrder, synchronizer.ErrPacketTooOld) {
					w.draining.Break()
					w.notifyPushSamples()
					return
				}
			}
		}
	}
}

func (w *AppWriter) pushPacket(pkt jitter.ExtPacket) error {
	w.translator.Translate(pkt.Packet)

	// get PTS
	pts, err := w.GetPTS(pkt)
	if err != nil {
		w.stats.packetsDropped.Inc()
		return err
	}

	if pts < 0 {
		// TODO: handle it by sending new gst segment that will reflect the offset
		w.logger.Debugw("negative packet pts, dropping", "pts", pts)
		w.stats.packetsDropped.Inc()
		return nil
	}

	p, err := pkt.Packet.Marshal()
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
		if flow == gst.FlowFlushing && w.flushDotRequested.CompareAndSwap(false, true) {
			w.callbacks.OnDebugDotRequest("appsrc_flush_" + w.track.ID())
		}
	}

	w.lastPushed.Store(time.Now())
	w.lastPTS = pts
	w.maybeCheckPipelineLag(pts)
	return nil
}

func (w *AppWriter) maybeCheckPipelineLag(pts time.Duration) {
	if pts-w.lastPipelineCheckPTS < pipelineCheckInterval {
		return
	}
	pipelineTime, ok := w.pipelineRunningTime()
	if !ok {
		return
	}
	w.lastPipelineCheckPTS = pts
	if pipelineTime <= w.conf.Latency.AudioMixerLatency {
		return
	}

	if pts < pipelineTime-w.conf.Latency.AudioMixerLatency {
		w.logger.Warnw(
			"packet PTS too far in the past compared to the pipeline, mixer will drop the buffer!",
			nil,
			"pts", pts,
			"pipelineRunningTime", pipelineTime,
		)
	}
}

func (w *AppWriter) Playing() {
	w.playing.Break()
}

// Drain blocks until finished
func (w *AppWriter) Drain(force bool) {
	w.draining.Once(func() {
		w.logger.Debugw("draining")

		endStream := func() {
			w.endStreamSignaled.Break()
			w.notifyPushSamples()
		}

		if force || !w.active.Load() {
			endStream()
		} else {
			time.AfterFunc(w.conf.Latency.PipelineLatency, endStream)
		}
	})

	<-w.finished.Watch()
	w.logger.Debugw("finished fuse broken")
	w.synchronizer.RemoveTrack(w.track.ID())
}

func (w *AppWriter) logStats() {
	ended := w.endStreamSignaled.Watch()
	ticker := time.NewTicker(time.Second * 10)
	defer ticker.Stop()

	for {
		select {
		case <-ended:
			stats := w.getStats()
			w.csvLogger.Write(stats)
			w.csvLogger.Close()
			w.logger.Debugw("appwriter stats ", "stats", stats, "requestType", w.conf.RequestType)
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

func (w *AppWriter) TrackKind() webrtc.RTPCodecType {
	return w.track.Kind()
}

func (w *AppWriter) drainJitterBuffer() {
	w.logger.Debugw("draining jitter buffer")
	w.buffer.Close()
	w.buffer.Flush()
	w.logger.Debugw("jitter buffer flushed")

	w.endStreamSourceProcessed.Break()
	w.notifyPushSamples()
}

func isDiscontinuity(lastPTS time.Duration, pts time.Duration) bool {
	return pts > lastPTS+discontinuityTolerance
}

func (w *AppWriter) shouldRemoveBeforeDrain() bool {
	return w.track.Kind() == webrtc.RTPCodecTypeVideo &&
		(w.conf.RequestType == types.RequestTypeParticipant || w.conf.RequestType == types.RequestTypeRoomComposite)
}

func (w *AppWriter) ensureRemovedBeforeDrain() {
	if w.shouldRemoveBeforeDrain() && w.removalRequested.CompareAndSwap(false, true) {
		w.callbacks.OnTrackRemoved(w.track.ID())
	}
}

type G711Packet struct{}

func (p *G711Packet) Unmarshal(packet []byte) ([]byte, error) {
	// G.711 payload is just the raw samples, return as-is (same as OpusPacket)
	if packet == nil {
		return nil, errors.New("nil packet")
	}
	return packet, nil
}

func (p *G711Packet) IsPartitionHead(_ []byte) bool {
	return true
}

func (p *G711Packet) IsPartitionTail(_ bool, _ []byte) bool {
	return true
}
