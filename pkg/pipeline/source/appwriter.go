package source

import (
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

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

	cs          *clockSync
	clockSynced bool
	tsOffset    int64
	nsOffset    int64
	conversion  int64

	playing chan struct{}
	closed  chan struct{}
}

// a single clockSync is shared between audio and video writers
type clockSync struct {
	startTime atomic.Int64
}

func (c *clockSync) GetOrSetStartTime(t int64) int64 {
	swapped := c.startTime.CAS(0, t)
	if swapped {
		return t
	}
	return c.startTime.Load()
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
		conversion: 1e9 / int64(track.Codec().ClockRate),
		playing:    playing,
		closed:     make(chan struct{}),
	}

	switch {
	case strings.EqualFold(track.Codec().MimeType, MimeTypeVP8):
		w.sb = samplebuilder.New(
			maxVideoLate, &codecs.VP8Packet{}, track.Codec().ClockRate,
			samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }),
		)

	case strings.EqualFold(track.Codec().MimeType, MimeTypeH264):
		w.sb = samplebuilder.New(
			maxVideoLate, &codecs.H264Packet{}, track.Codec().ClockRate,
			samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }),
		)

	case strings.EqualFold(track.Codec().MimeType, MimeTypeOpus):
		w.sb = samplebuilder.New(maxAudioLate, &codecs.OpusPacket{}, track.Codec().ClockRate)

	default:
		return nil, errors.ErrNotSupported(track.Codec().MimeType)
	}

	go w.start()
	return w, nil
}

func (w *appWriter) start() {
	for {
		pkt, _, err := w.track.ReadRTP()
		if err != nil {
			if err.Error() == "EOF" {
				// TODO: better handling (when can/does this happen?)
				continue
			}

			w.logger.Errorw("could not read from track", err)
			return
		}

		if !w.clockSynced {
			// clock sync - see comment in writeRTP
			now := time.Now().UnixNano()
			startTime := w.cs.GetOrSetStartTime(now)
			w.nsOffset = now - startTime
			w.tsOffset = int64(pkt.Timestamp)
			w.clockSynced = true
		}

		w.sb.Push(pkt)

		select {
		case <-w.closed:
			w.src.EndStream()
			return

		case <-w.playing:
			for _, p := range w.sb.PopPackets() {
				if err = w.writeRTP(p); err != nil {
					w.logger.Errorw("could not write to file", err)
					return
				}
			}

		default:
			continue
		}
	}
}

func (w *appWriter) writeRTP(pkt *rtp.Packet) error {
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
	nanoSecondsElapsed := cyclesElapsed * w.conversion
	b.SetPresentationTimestamp(time.Duration(nanoSecondsElapsed + w.nsOffset))

	w.src.PushBuffer(b)
	return nil
}

func (w *appWriter) stop() {
	select {
	case <-w.closed:
		return
	default:
		close(w.closed)
	}
}
