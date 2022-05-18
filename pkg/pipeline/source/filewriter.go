package source

import (
	"io"
	"time"

	"github.com/go-logr/logr"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"

	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/livekit/server-sdk-go/pkg/media/ivfwriter"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
)

type fileWriter struct {
	sb      *samplebuilder.SampleBuilder
	track   *webrtc.TrackRemote
	cs      *clockSync
	maxLate time.Duration

	started bool

	logger   logger.Logger
	writer   media.Writer
	drain    chan struct{}
	finished chan struct{}
}

func newFileWriter(
	p *params.Params,
	track *webrtc.TrackRemote,
	codec params.MimeType,
	rp *lksdk.RemoteParticipant,
	l logger.Logger,
	cs *clockSync,
) (*fileWriter, error) {

	w := &fileWriter{
		track:    track,
		cs:       cs,
		logger:   logger.Logger(logr.Logger(l).WithValues("trackID", track.ID())),
		drain:    make(chan struct{}),
		finished: make(chan struct{}),
	}

	var err error
	switch codec {
	case params.MimeTypeVP8:
		writer, err := ivfwriter.New(p.Filename)
		if err != nil {
			return nil, err
		}

		w.writer = writer
		w.sb = samplebuilder.New(
			maxVideoLate, &codecs.VP8Packet{}, track.Codec().ClockRate,
			samplebuilder.WithPacketDroppedHandler(func() {
				writer.FrameDropped()
				rp.WritePLI(track.SSRC())
			}),
		)

	default:
		err = errors.ErrNotSupported(track.Codec().MimeType)
	}
	if err != nil {
		return nil, err
	}

	go w.start()
	return w, nil
}

func (w *fileWriter) start() {
	defer func() {
		if err := w.writer.Close(); err != nil {
			w.logger.Errorw("could not close file writer", err)
		}

		close(w.finished)
	}()

	for {
		select {
		case <-w.drain:
			// drain sample builder
			_ = w.writePackets(true)
			return

		default:
			pkt, _, err := w.track.ReadRTP()
			if err != nil {
				if errors.Is(err, io.EOF) {
					w.stop()
					continue
				} else {
					w.logger.Errorw("could not read from track", err)
					return
				}
			}

			if !w.started {
				w.cs.GetOrSetStartTime(time.Now().UnixNano())
				w.started = true
			}

			w.sb.Push(pkt)
			if err = w.writePackets(false); err != nil {
				return
			}
		}
	}
}

func (w *fileWriter) writePackets(force bool) error {
	var pkts []*rtp.Packet
	if force {
		pkts = w.sb.ForcePopPackets()
	} else {
		pkts = w.sb.PopPackets()
	}

	for _, pkt := range pkts {
		if err := w.writer.WriteRTP(pkt); err != nil {
			w.logger.Errorw("could not write to file", err)
			return err
		}
	}

	return nil
}

func (w *fileWriter) trackMuted() {
	w.logger.Debugw("track muted")
	// TODO: start writing blank frames
}

func (w *fileWriter) trackUnmuted() {
	w.logger.Debugw("track unmuted")
	// TODO: go back to reading from track
}

// stop blocks until finished
func (w *fileWriter) stop() {
	select {
	case <-w.drain:
	default:
		close(w.drain)
	}

	<-w.finished
}
