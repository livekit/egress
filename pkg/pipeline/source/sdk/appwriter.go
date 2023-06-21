package sdk

import (
	"fmt"
	"io"
	"math"
	"net"
	"time"

	"github.com/frostbyte73/core"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/livekit/server-sdk-go/pkg/jitter"
	"github.com/livekit/server-sdk-go/pkg/synchronizer"
)

type state int

const (
	statePlaying state = iota
	stateMuted
	stateUnmuting
)

const (
	latency           = time.Second * 2
	drainTimeout      = time.Second * 4
	errBufferTooSmall = "buffer too small"
)

type AppWriter struct {
	logger      logger.Logger
	track       *webrtc.TrackRemote
	identity    string
	codec       types.MimeType
	src         *app.Source
	startTime   time.Time
	writeBlanks bool

	buffer     *jitter.Buffer
	translator Translator
	sendPLI    func()

	// a/v sync
	sync *synchronizer.Synchronizer
	*synchronizer.TrackSynchronizer

	// state
	state       state
	initialized bool
	ticker      *time.Ticker
	muted       atomic.Bool
	playing     core.Fuse
	draining    core.Fuse
	endStream   core.Fuse
	finished    core.Fuse
}

func NewAppWriter(
	track *webrtc.TrackRemote,
	rp *lksdk.RemoteParticipant,
	codec types.MimeType,
	src *app.Source,
	sync *synchronizer.Synchronizer,
	syncInfo *synchronizer.TrackSynchronizer,
	writeBlanks bool,
) (*AppWriter, error) {
	w := &AppWriter{
		logger:            logger.GetLogger().WithValues("trackID", track.ID(), "kind", track.Kind().String()),
		track:             track,
		identity:          rp.Identity(),
		codec:             codec,
		src:               src,
		writeBlanks:       writeBlanks,
		sync:              sync,
		TrackSynchronizer: syncInfo,
		playing:           core.NewFuse(),
		draining:          core.NewFuse(),
		endStream:         core.NewFuse(),
		finished:          core.NewFuse(),
	}

	var depacketizer rtp.Depacketizer
	switch codec {
	case types.MimeTypeVP8:
		depacketizer = &codecs.VP8Packet{}
		w.translator = NewVP8Translator(w.logger)
		w.sendPLI = func() { rp.WritePLI(track.SSRC()) }

	case types.MimeTypeH264:
		depacketizer = &codecs.H264Packet{}
		w.translator = NewH264Translator()
		w.sendPLI = func() { rp.WritePLI(track.SSRC()) }

	case types.MimeTypeOpus:
		depacketizer = &codecs.OpusPacket{}
		w.translator = NewOpusTranslator()

	default:
		return nil, errors.ErrNotSupported(track.Codec().MimeType)
	}

	w.buffer = jitter.NewBuffer(
		depacketizer,
		track.Codec().ClockRate,
		latency,
		jitter.WithPacketDroppedHandler(w.sendPLI),
		jitter.WithLogger(w.logger),
	)

	go w.run()
	return w, nil
}

func (w *AppWriter) Play() {
	w.playing.Break()
}

func (w *AppWriter) SetTrackMuted(muted bool) {
	w.muted.Store(muted)
	if muted {
		w.logger.Debugw("track muted", "timestamp", time.Since(w.startTime).Seconds())
	} else {
		w.logger.Debugw("track unmuted", "timestamp", time.Since(w.startTime).Seconds())
		if w.sendPLI != nil {
			w.sendPLI()
		}
	}
}

// Drain blocks until finished
func (w *AppWriter) Drain(force bool) {
	w.draining.Once(func() {
		w.logger.Debugw("draining")

		if force {
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
		case statePlaying:
			w.handlePlaying()
		case stateMuted:
			w.handleMuted()
		case stateUnmuting:
			w.handleUnmute()
		}
	}

	// clean up
	_ = w.pushSamples(true)
	if w.playing.IsBroken() {
		if flow := w.src.EndStream(); flow != gst.FlowOK && flow != gst.FlowFlushing {
			w.logger.Errorw("unexpected flow return", nil, "flowReturn", flow.String())
		}
	}

	stats := w.GetTrackStats()
	loss := w.buffer.PacketLoss()
	w.logger.Infow("writer finished",
		"sample duration rtp", math.Round(stats.AvgSampleDuration),
		"sample duration ns", w.GetFrameDuration(),
		"avg drift", time.Duration(stats.AvgDrift),
		"max drift", stats.MaxDrift,
		"packet loss", fmt.Sprintf("%.2f%", loss),
	)

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
	if err = w.pushSamples(false); err != nil {
		w.endStream.Break()
	}
}

func (w *AppWriter) handleMuted() {
	switch {
	case w.draining.IsBroken():
		w.ticker.Stop()
		w.endStream.Break()

	case !w.muted.Load():
		w.ticker.Stop()
		if w.writeBlanks {
			w.state = stateUnmuting
		} else {
			w.state = statePlaying
		}

	default:
		<-w.ticker.C
		if w.writeBlanks {
			_, err := w.insertBlankFrame(nil)
			if err != nil {
				w.ticker.Stop()
				w.endStream.Break()
			}
		}
	}
}

func (w *AppWriter) handleUnmute() {
	_ = w.track.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
	pkt, _, err := w.track.ReadRTP()
	if err != nil {
		w.handleReadError(err)
		return
	}

	// the blank frames will be ~500ms behind and need to fill the gap
	for !w.endStream.IsBroken() {
		ok, err := w.insertBlankFrame(pkt)
		if err != nil {
			w.endStream.Break()
			return
		} else if !ok {
			// push packet to sample builder and return
			w.buffer.Push(pkt)
			w.state = statePlaying
			return
		}
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

	// check if muted
	if w.muted.Load() {
		_ = w.pushSamples(true)
		w.ticker = time.NewTicker(w.GetFrameDuration())
		w.state = stateMuted
		return
	}

	// continue on timeout
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return
	}

	// log non-EOF errors
	if !errors.Is(err, io.EOF) {
		w.logger.Errorw("could not read packet", err)
	}

	// end stream
	w.endStream.Break()
}

func (w *AppWriter) insertBlankFrame(next *rtp.Packet) (bool, error) {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:     2,
			Padding:     false,
			Marker:      true,
			PayloadType: uint8(w.track.PayloadType()),
			SSRC:        uint32(w.track.SSRC()),
			CSRC:        []uint32{},
		},
	}

	var pts time.Duration
	if next != nil {
		var ok bool
		pts, ok = w.InsertFrameBefore(pkt, next)
		if !ok {
			return false, nil
		}
	} else {
		pts = w.InsertFrame(pkt)
	}

	if err := w.translator.UpdateBlankFrame(pkt); err != nil {
		return false, err
	}

	if err := w.pushPacket(pkt, pts); err != nil {
		return false, err
	}

	return true, nil
}

func (w *AppWriter) pushSamples(force bool) error {
	// buffers can only be pushed to the appsrc while in the playing state
	if !w.playing.IsBroken() {
		return nil
	}

	pkts := w.buffer.Pop(force)
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
	b.SetPresentationTimestamp(pts)
	if flow := w.src.PushBuffer(b); flow != gst.FlowOK {
		w.logger.Infow("unexpected flow return", "flow", flow)
	}

	return nil
}
