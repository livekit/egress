package source

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/h264writer"
	"github.com/pion/webrtc/v3/pkg/media/ivfwriter"
	"github.com/pion/webrtc/v3/pkg/media/oggwriter"

	"github.com/livekit/livekit-egress/pkg/pipeline/params"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"

	"github.com/livekit/livekit-egress/pkg/errors"
)

type fileWriter struct {
	sb      *samplebuilder.SampleBuilder
	track   *webrtc.TrackRemote
	cs      *clockSync
	maxLate time.Duration

	started bool
	logger  logger.Logger
	writer  media.Writer
	closed  chan struct{}
}

func newFileWriter(p *params.Params, track *webrtc.TrackRemote, rp *lksdk.RemoteParticipant, l logger.Logger, cs *clockSync) (*fileWriter, error) {
	w := &fileWriter{
		track:  track,
		cs:     cs,
		logger: logger.Logger(logr.Logger(l).WithValues("trackID", track.ID())),
		closed: make(chan struct{}),
	}

	filename := p.Filepath
	if filename == "" || strings.HasSuffix(filename, "/") {
		filename = fmt.Sprintf("%s%s-%v", filename, track.ID(), time.Now().String())
	}
	var err error

	switch {
	case strings.EqualFold(track.Codec().MimeType, MimeTypeVP8):
		w.sb = samplebuilder.New(
			maxVideoLate, &codecs.VP8Packet{}, track.Codec().ClockRate,
			samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }),
		)
		if !strings.HasSuffix(filename, ".ivf") {
			filename = filename + ".ivf"
		}
		w.writer, err = ivfwriter.New(filename)

	case strings.EqualFold(track.Codec().MimeType, MimeTypeH264):
		w.sb = samplebuilder.New(
			maxVideoLate, &codecs.H264Packet{}, track.Codec().ClockRate,
			samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }),
		)
		if !strings.HasSuffix(filename, ".h264") {
			filename = filename + ".h264"
		}
		w.writer, err = h264writer.New(filename)

	case strings.EqualFold(track.Codec().MimeType, MimeTypeOpus):
		w.sb = samplebuilder.New(maxAudioLate, &codecs.OpusPacket{}, track.Codec().ClockRate)
		if !strings.HasSuffix(filename, ".ogg") {
			filename = filename + ".ogg"
		}
		w.writer, err = oggwriter.New(filename, 48000, track.Codec().Channels)

	default:
		err = errors.ErrNotSupported(track.Codec().MimeType)
	}
	if err != nil {
		return nil, err
	}

	p.Filename = filename
	p.FileInfo.Filename = filename

	go w.start()
	return w, nil
}

func (w *fileWriter) start() {
	defer func() {
		if err := w.writer.Close(); err != nil {
			w.logger.Errorw("could not close file writer", err)
		}
	}()

	for {
		select {
		case <-w.closed:
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

func (w *fileWriter) stop() {
	select {
	case <-w.closed:
		return
	default:
		close(w.closed)
	}
}
