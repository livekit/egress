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

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"
)

type appWriter struct {
	sb    *samplebuilder.SampleBuilder
	track *webrtc.TrackRemote
	src   *app.Source

	logger     logger.Logger
	runtime    time.Time
	runtimeSet bool
	playing    chan struct{}
	closed     chan struct{}
}

func newAppWriter(
	track *webrtc.TrackRemote,
	rp *lksdk.RemoteParticipant,
	l logger.Logger,
	src *app.Source,
	playing chan struct{},
) (*appWriter, error) {

	w := &appWriter{
		track:   track,
		src:     src,
		logger:  logger.Logger(logr.Logger(l).WithValues("trackID", track.ID())),
		playing: playing,
		closed:  make(chan struct{}),
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
				continue
			}

			w.logger.Errorw("could not read from track", err)
			return
		}
		w.sb.Push(pkt)

		select {
		case <-w.playing:
			for _, p := range w.sb.PopPackets() {
				if err = w.writeRTP(p); err != nil {
					w.logger.Errorw("could not write to file", err)
					return
				}
			}
		case <-w.closed:
			return
		default:
			continue
		}
	}
}

func (w *appWriter) writeRTP(pkt *rtp.Packet) error {
	// Set run time
	if !w.runtimeSet {
		w.runtime = time.Now()
		w.runtimeSet = true
	}

	p, err := pkt.Marshal()
	if err != nil {
		return err
	}

	// Parse packet timestamp
	ts := time.Unix(int64(pkt.Timestamp), 0)

	// Create buffer and set PTS
	b := gst.NewBufferFromBytes(p)
	d := ts.Sub(w.runtime)
	b.SetPresentationTimestamp(d)

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
