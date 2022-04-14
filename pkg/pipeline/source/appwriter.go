package source

import (
	"io"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

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
	tsOffset    int64
	nsOffset    int64
	conversion  float64
	maxLate     time.Duration
	maxTS       int64

	// state
	playing  chan struct{}
	close    chan struct{}
	draining bool
	finished chan struct{}
}

func newAppWriter(
	track *webrtc.TrackRemote,
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
		close:      make(chan struct{}),
		finished:   make(chan struct{}),
	}

	switch {
	case strings.EqualFold(track.Codec().MimeType, MimeTypeVP8):
		w.sb = samplebuilder.New(
			maxVideoLate, &codecs.VP8Packet{}, track.Codec().ClockRate,
			samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }),
		)
		w.maxLate = time.Second * 2

	case strings.EqualFold(track.Codec().MimeType, MimeTypeH264):
		w.sb = samplebuilder.New(
			maxVideoLate, &codecs.H264Packet{}, track.Codec().ClockRate,
			samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }),
		)
		w.maxLate = time.Second * 2

	case strings.EqualFold(track.Codec().MimeType, MimeTypeOpus):
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
				logger.Errorw("unexpected flow return", nil, "flowReturn", flow.String())
			}
		}

		close(w.finished)
	}()

	// used to force push any remaining packets when the pipeline is closing
	force := make(chan struct{})

	for {
		// if close was requested, start draining
		if w.isClosing() && !w.draining {
			if !w.isPlaying() {
				return
			}

			w.logger.Debugw("draining")
			w.draining = true

			// get sync info
			startTime := w.cs.GetStartTime()
			endTime := w.cs.GetEndTime()
			delay := w.cs.GetDelay()

			// get the expected timestamp of the last packet
			nanoSecondsElapsed := endTime - startTime + delay - w.nsOffset
			cyclesElapsed := int64(float64(nanoSecondsElapsed) / w.conversion)
			w.maxTS = cyclesElapsed + w.tsOffset

			// wait until maxLate before force popping
			time.AfterFunc(w.maxLate, func() { close(force) })
		}

		select {
		case <-force:
			// force push remaining packets and quit
			w.logger.Debugw("force timeout exceeded")
			_ = w.pushBuffers(true)
			return
		default:
			// continue
		}

		// read next packet
		pkt, _, err := w.track.ReadRTP()
		if err != nil {
			// no more data - if closing, force push and quit
			w.logger.Debugw("force push and close", "reason", err.Error())
			_ = w.pushBuffers(true)
			return
		}

		// sync offsets after first packet read
		// see comment in writeRTP below
		if !w.clockSynced {
			now := time.Now().UnixNano()
			startTime := w.cs.GetOrSetStartTime(now)
			w.nsOffset = now - startTime
			w.tsOffset = int64(pkt.Timestamp)
			w.clockSynced = true
		}

		// push packet to sample builder
		w.sb.Push(pkt)

		// push completed packets to appsrc
		if err = w.pushBuffers(false); err != nil {
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
		if w.draining && int64(pkt.Timestamp) >= w.maxTS {
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
		// The conversion is done by subtracting the initial RTP timestamp (w.tsOffset) from the current RTP timestamp
		// and multiplying by a conversion rate of (1e9 ns/s / clock rate).
		// Since the audio and video track might start pushing to their buffers at different times, we then add a
		// synced clock offset (w.nsOffset), which is always 0 for the first track, and fixes the video starting to play too
		// early if it's waiting for a key frame
		cyclesElapsed := int64(pkt.Timestamp) - w.tsOffset
		nanoSecondsElapsed := int64(float64(cyclesElapsed) * w.conversion)
		b.SetPresentationTimestamp(time.Duration(nanoSecondsElapsed + w.nsOffset))

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

func (w *appWriter) stop() {
	select {
	case <-w.close:
		return
	default:
		close(w.close)
	}
}

func (w *appWriter) isClosing() bool {
	select {
	case <-w.close:
		return true
	default:
		return false
	}
}

func (w *appWriter) waitUntilFinished() {
	<-w.finished
}
