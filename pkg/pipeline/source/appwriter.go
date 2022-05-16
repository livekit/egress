package source

import (
	"io"
	"net"
	"time"

	"github.com/go-logr/logr"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/livekit-server/pkg/sfu"
	"github.com/livekit/livekit-server/pkg/sfu/buffer"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"

	"github.com/livekit/livekit-egress/pkg/errors"
)

type appWriter struct {
	logger logger.Logger
	sb     *samplebuilder.SampleBuilder
	track  *webrtc.TrackRemote
	codec  params.MimeType
	src    *app.Source

	// a/v sync
	cs              *clockSync
	clockSynced     bool
	rtpOffset       int64
	ptsOffset       int64
	snOffset        uint16
	conversion      float64
	lastSN          uint16
	lastTS          uint32
	maxLate         time.Duration
	maxRTP          atomic.Int64
	lastPictureId   uint16
	pictureIdOffset uint16

	// state
	muted    atomic.Bool
	playing  chan struct{}
	drain    chan struct{}
	force    chan struct{}
	finished chan struct{}

	newSampleBuffer func() *samplebuilder.SampleBuilder

	vp8Munger      *sfu.VP8Munger
	firstPktPushed bool
	startTime      time.Time
}

func newAppWriter(
	track *webrtc.TrackRemote,
	codec params.MimeType,
	rp *lksdk.RemoteParticipant,
	l logger.Logger,
	src *app.Source,
	cs *clockSync,
	playing chan struct{},
) (*appWriter, error) {

	w := &appWriter{
		logger:     logger.Logger(logr.Logger(l).WithValues("trackID", track.ID(), "kind", track.Kind().String())),
		track:      track,
		codec:      codec,
		src:        src,
		cs:         cs,
		conversion: 1e9 / float64(track.Codec().ClockRate),
		playing:    playing,
		drain:      make(chan struct{}),
		force:      make(chan struct{}),
		finished:   make(chan struct{}),
	}

	switch codec {
	case params.MimeTypeVP8:
		w.newSampleBuffer = func() *samplebuilder.SampleBuilder {
			return samplebuilder.New(
				maxVideoLate, &codecs.VP8Packet{}, track.Codec().ClockRate,
				samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }),
			)
		}
		w.sb = w.newSampleBuffer()
		w.maxLate = time.Second * 2
		w.vp8Munger = sfu.NewVP8Munger(w.logger)

	case params.MimeTypeH264:
		w.newSampleBuffer = func() *samplebuilder.SampleBuilder {
			return samplebuilder.New(
				maxVideoLate, &codecs.H264Packet{}, track.Codec().ClockRate,
				samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }),
			)
		}
		w.sb = w.newSampleBuffer()
		w.maxLate = time.Second * 2

	case params.MimeTypeOpus:
		w.newSampleBuffer = func() *samplebuilder.SampleBuilder {
			return samplebuilder.New(maxAudioLate, &codecs.OpusPacket{}, track.Codec().ClockRate)
		}
		w.sb = w.newSampleBuffer()
		w.maxLate = time.Second * 4

	default:
		return nil, errors.ErrNotSupported(track.Codec().MimeType)
	}

	go w.start()
	return w, nil
}

func (w *appWriter) start() {
	// always post EOS if the writer started playing
	defer func() {
		if w.isPlaying() {
			if flow := w.src.EndStream(); flow != gst.FlowOK {
				w.logger.Errorw("unexpected flow return", nil, "flowReturn", flow.String())
			}
		}

		close(w.finished)
	}()

	w.startTime = time.Now()

	for {
		if w.isDraining() && !w.isPlaying() {
			// quit if draining but not yet playing
			return
		}

		select {
		case <-w.force:
			// force push remaining packets and quit
			_ = w.pushPackets(true)
			return
		default:
			// continue
		}

		// read next packet
		_ = w.track.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
		pkt, _, err := w.track.ReadRTP()
		if err != nil {
			if w.muted.Load() {
				// switch to pushing blank frames until unmuted
				err = w.pushBlankFrame()
				if err == nil {
					continue
				}
			}

			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// continue if read timeout
				continue
			}

			if !errors.Is(err, io.EOF) {
				w.logger.Errorw("could not read packet", err)
			}

			// force push remaining packets and quit
			_ = w.pushPackets(true)
			return
		}

		// sync offsets after first packet read
		// see comment in writeRTP below
		if !w.clockSynced {
			now := time.Now().UnixNano()
			startTime := w.cs.GetOrSetStartTime(now)
			w.ptsOffset = now - startTime
			w.rtpOffset = int64(pkt.Timestamp)
			w.clockSynced = true
		}

		// update sequence number, record SN and TS
		pkt.SequenceNumber += w.snOffset
		w.lastSN = pkt.SequenceNumber
		w.lastTS = pkt.Timestamp

		// push packet to sample builder
		w.sb.Push(pkt)

		// push completed packets to appsrc
		if err = w.pushPackets(false); err != nil {
			if !errors.Is(err, io.EOF) {
				w.logger.Errorw("could not push buffers", err)
			}
			return
		}
	}
}

func (w *appWriter) pushPackets(force bool) error {
	// buffers can only be pushed to the appsrc while in the playing state
	if !w.isPlaying() {
		return nil
	}

	if force {
		return w.push(w.sb.ForcePopPackets(), false)
	} else {
		return w.push(w.sb.PopPackets(), false)
	}
}

func (w *appWriter) pushBlankFrame() error {
	_ = w.pushPackets(true)

	// TODO: sample buffer has bug that it may pop old packet after pushPackets(true)
	// recreated it to work now, will remove this when bug fixed
	w.sb = w.newSampleBuffer()

	if !w.muted.Load() || w.isDraining() {
		return nil
	}

	pkt, err := w.getBlankFrame()
	if err != nil {
		return err
	}

	err = w.push([]*rtp.Packet{pkt}, true)
	if err != nil {
		return err
	}

	return nil
}

func (w *appWriter) push(packets []*rtp.Packet, blankFrame bool) error {
	for _, pkt := range packets {
		if !blankFrame {
			w.translatePacket(pkt)
		}

		if w.isDraining() && int64(pkt.Timestamp) >= w.maxRTP.Load() {
			return io.EOF
		}

		p, err := pkt.Marshal()
		if err != nil {
			return err
		}

		b := gst.NewBufferFromBytes(p)

		// RTP packet timestamps start at a random number, and increase according to clock rate (for example, with a
		// clock rate of 90kHz, the timestamp will increase by 90000 every second).
		// The GStreamer clock time also starts at a random number, and increases in nanoseconds.
		// The conversion is done by subtracting the initial RTP timestamp (w.rtpOffset) from the current RTP timestamp
		// and multiplying by a conversion rate of (1e9 ns/s / clock rate).
		// Since the audio and video track might start pushing to their buffers at different times, we then add a
		// synced clock offset (w.ptsOffset), which is always 0 for the first track, and fixes the video starting to play too
		// early if it's waiting for a key frame
		cyclesElapsed := int64(pkt.Timestamp) - w.rtpOffset
		nanoSecondsElapsed := int64(float64(cyclesElapsed) * w.conversion)
		b.SetPresentationTimestamp(time.Duration(nanoSecondsElapsed + w.ptsOffset))

		w.src.PushBuffer(b)
	}

	return nil
}

func (w *appWriter) translatePacket(pkt *rtp.Packet) {
	switch w.codec {
	case params.MimeTypeVP8:
		ep := &buffer.ExtPacket{
			Packet:  pkt,
			Arrival: time.Now().UnixNano(),
			VideoLayer: buffer.VideoLayer{
				Spatial:  -1,
				Temporal: -1,
			},
		}
		ep.Temporal = 0

		vp8Packet := buffer.VP8{}
		if err := vp8Packet.Unmarshal(pkt.Payload); err != nil {
			w.logger.Warnw("could not unmarshal VP8 packet", err)
			return
		}
		ep.Payload = vp8Packet
		ep.KeyFrame = vp8Packet.IsKeyFrame
		ep.Temporal = int32(vp8Packet.TID)

		if !w.firstPktPushed {
			w.firstPktPushed = true
			w.vp8Munger.SetLast(ep)
		} else {
			tpVP8, err := w.vp8Munger.UpdateAndGet(ep, sfu.SequenceNumberOrderingContiguous, ep.Temporal)
			if err != nil {
				w.logger.Warnw("could not update VP8 packet", err)
				return
			}

			payload := pkt.Payload
			payload, err = w.translateVP8PacketTo(ep.Packet, &vp8Packet, tpVP8.Header, &payload)
			if err != nil {
				w.logger.Warnw("could not translate VP8 packet", err)
				return
			}
			pkt.Payload = payload
		}

	default:
		return
	}
}

func (w *appWriter) translateVP8PacketTo(pkt *rtp.Packet, incomingVP8 *buffer.VP8, translatedVP8 *buffer.VP8, outbuf *[]byte) ([]byte, error) {
	var buf []byte
	if outbuf == &pkt.Payload {
		buf = pkt.Payload
	} else {
		buf = (*outbuf)[:len(pkt.Payload)+translatedVP8.HeaderSize-incomingVP8.HeaderSize]

		srcPayload := pkt.Payload[incomingVP8.HeaderSize:]
		dstPayload := buf[translatedVP8.HeaderSize:]
		copy(dstPayload, srcPayload)
	}

	err := translatedVP8.MarshalTo(buf[:translatedVP8.HeaderSize])
	return buf, err
}

func (w *appWriter) isPlaying() bool {
	select {
	case <-w.playing:
		return true
	default:
		return false
	}
}

func (w *appWriter) isDraining() bool {
	select {
	case <-w.drain:
		return true
	default:
		return false
	}
}

func (w *appWriter) trackMuted() {
	w.logger.Debugw("track muted", "timestamp", time.Since(w.startTime).Seconds())
	w.muted.Store(true)
}

func (w *appWriter) trackUnmuted() {
	w.logger.Debugw("track unmuted", "timestamp", time.Since(w.startTime).Seconds())
	w.muted.Store(false)
}

// stop blocks until finished
func (w *appWriter) stop() {
	select {
	case <-w.drain:
	default:
		w.logger.Debugw("draining")

		// get sync info
		startTime := w.cs.GetStartTime()
		endTime := w.cs.GetEndTime()
		delay := w.cs.GetDelay()

		// get the expected timestamp of the last packet
		nanoSecondsElapsed := endTime - startTime + delay - w.ptsOffset
		cyclesElapsed := int64(float64(nanoSecondsElapsed) / w.conversion)
		w.maxRTP.Store(cyclesElapsed + w.rtpOffset)

		// start draining
		close(w.drain)

		// wait until maxLate before force popping
		time.AfterFunc(w.maxLate, func() { close(w.force) })
	}

	<-w.finished
}
