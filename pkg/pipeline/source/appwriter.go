package source

import (
	"io"
	"time"

	"github.com/go-logr/logr"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"

	"github.com/livekit/livekit-egress/pkg/errors"
)

type appWriter struct {
	logger logger.Logger
	sb     *samplebuilder.SampleBuilder
	track  *webrtc.TrackRemote
	src    *app.Source

	// a/v sync
	cs          *clockSync
	clockSynced bool
	rtpOffset   int64
	ptsOffset   int64
	conversion  float64
	maxLate     time.Duration
	maxRTP      atomic.Int64

	// state
	playing  chan struct{}
	drain    chan struct{}
	force    chan struct{}
	finished chan struct{}
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
		w.sb = samplebuilder.New(
			maxVideoLate, &codecs.VP8Packet{}, track.Codec().ClockRate,
			samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }),
		)
		w.maxLate = time.Second * 2

	case params.MimeTypeH264:
		w.sb = samplebuilder.New(
			maxVideoLate, &codecs.H264Packet{}, track.Codec().ClockRate,
			samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }),
		)
		w.maxLate = time.Second * 2

	case params.MimeTypeOpus:
		w.sb = samplebuilder.New(maxAudioLate, &codecs.OpusPacket{}, track.Codec().ClockRate)
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

	for {
		if w.isDraining() && !w.isPlaying() {
			// quit if draining but not yet playing
			return
		}

		select {
		case <-w.force:
			// force push remaining packets and quit
			_ = w.pushBuffers(true)
			return
		default:
			// continue
		}

		// read next packet
		pkt, _, err := w.track.ReadRTP()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				w.logger.Errorw("could not read packet", err)
			}

			// force push remaining packets and quit
			_ = w.pushBuffers(true)
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

		// push packet to sample builder
		w.sb.Push(pkt)

		// push completed packets to appsrc
		if err = w.pushBuffers(false); err != nil {
			if !errors.Is(err, io.EOF) {
				w.logger.Errorw("could not push buffers", err)
			}
			return
		}
	}
}

func (w *appWriter) pushBuffers(force bool) error {
	// buffers can only be pushed to the appsrc while in the playing state
	if !w.isPlaying() {
		return nil
	}

	var packets []*rtp.Packet
	if force {
		packets = w.sb.ForcePopPackets()
	} else {
		packets = w.sb.PopPackets()
	}

	for _, pkt := range packets {
		if w.isDraining() && int64(pkt.Timestamp) >= w.maxRTP.Load() {
			return io.EOF
		}

		p, err := pkt.Marshal()
		if err != nil {
			return err
		}

		// create buffer
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
