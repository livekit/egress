package source

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/h264writer"
	"github.com/pion/webrtc/v3/pkg/media/ivfwriter"
	"github.com/pion/webrtc/v3/pkg/media/oggwriter"

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"
)

type fileWriter struct {
	sb    *samplebuilder.SampleBuilder
	track *webrtc.TrackRemote

	logger logger.Logger
	writer media.Writer
	closed chan struct{}
}

func newFileWriter(track *webrtc.TrackRemote, rp *lksdk.RemoteParticipant, l logger.Logger) (*fileWriter, error) {
	w := &fileWriter{
		track:  track,
		logger: logger.Logger(logr.Logger(l).WithValues("trackID", track.ID())),
		closed: make(chan struct{}),
	}

	filename := fmt.Sprintf("%s-%v", track.ID(), time.Now().String())
	var err error

	switch {
	case strings.EqualFold(track.Codec().MimeType, MimeTypeVP8):
		w.sb = samplebuilder.New(
			maxVideoLate, &codecs.VP8Packet{}, track.Codec().ClockRate,
			samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }),
		)
		w.writer, err = ivfwriter.New(filename + ".ivf")

	case strings.EqualFold(track.Codec().MimeType, MimeTypeH264):
		w.sb = samplebuilder.New(
			maxVideoLate, &codecs.H264Packet{}, track.Codec().ClockRate,
			samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }),
		)
		w.writer, err = h264writer.New(filename + ".h264")

	case strings.EqualFold(track.Codec().MimeType, MimeTypeOpus):
		w.sb = samplebuilder.New(maxAudioLate, &codecs.OpusPacket{}, track.Codec().ClockRate)
		w.writer, err = oggwriter.New(filename+".ogg", 48000, track.Codec().Channels)

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
		err := w.writer.Close()
		if err != nil {
			w.logger.Errorw("could not close file writer", err)
		}
	}()

	started := false
	for {
		select {
		case <-w.closed:
			return
		default:
			pkt, _, err := w.track.ReadRTP()
			if err != nil {
				if !started && err.Error() == "EOF" {
					time.Sleep(time.Millisecond * 100)
					continue
				}
				w.logger.Errorw("could not read from track", err)
				return
			} else if !started {
				started = true
			}

			w.sb.Push(pkt)
			for _, p := range w.sb.PopPackets() {
				if err = w.writer.WriteRTP(p); err != nil {
					w.logger.Errorw("could not write to file", err)
					return
				}
			}
		}
	}
}

func (w *fileWriter) stop() {
	select {
	case <-w.closed:
		return
	default:
		close(w.closed)
	}
}
