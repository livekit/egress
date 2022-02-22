package source

import (
	"strings"
	"sync"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"go.uber.org/atomic"

	"github.com/livekit/livekit-egress/pkg/errors"
	"github.com/livekit/livekit-egress/pkg/pipeline/params"
)

const (
	maxVideoLate = 1000 // nearly 2s for fhd video
	maxAudioLate = 200  // 4s for audio
)

type SDKSource struct {
	mu sync.Mutex

	room     *lksdk.Room
	trackIDs []string
	active   atomic.Int32
	writers  map[string]*trackWriter

	endRecording chan struct{}
}

func NewSDKSource(p *params.Params, createWriter func(*webrtc.TrackRemote) (media.Writer, error)) (*SDKSource, error) {
	s := &SDKSource{
		room:         lksdk.CreateRoom(p.LKUrl),
		endRecording: make(chan struct{}),
	}

	switch p.Info.Request.(type) {
	case *livekit.EgressInfo_TrackComposite:
		s.trackIDs = []string{p.AudioTrackID, p.VideoTrackID}
	default:
		s.trackIDs = []string{p.TrackID}
	}

	s.room.Callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		var sb *samplebuilder.SampleBuilder
		switch {
		case strings.EqualFold(track.Codec().MimeType, "video/vp8"):
			sb = samplebuilder.New(maxVideoLate, &codecs.VP8Packet{}, track.Codec().ClockRate,
				samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }))
		case strings.EqualFold(track.Codec().MimeType, "video/h264"):
			sb = samplebuilder.New(maxVideoLate, &codecs.H264Packet{}, track.Codec().ClockRate,
				samplebuilder.WithPacketDroppedHandler(func() { rp.WritePLI(track.SSRC()) }))
		case strings.EqualFold(track.Codec().MimeType, "audio/opus"):
			sb = samplebuilder.New(maxAudioLate, &codecs.OpusPacket{}, track.Codec().ClockRate)
		default:
			logger.Errorw("could not record track", errors.ErrNotSupported(track.Codec().MimeType))
			return
		}

		mw, err := createWriter(track)
		if err != nil {
			logger.Errorw("could not record track", err)
		}

		tw := &trackWriter{
			sb:     sb,
			writer: mw,
			track:  track,
			closed: make(chan struct{}),
		}
		go tw.start()

		s.mu.Lock()
		s.writers[track.ID()] = tw
		s.mu.Unlock()
	}
	s.room.Callback.OnTrackUnpublished = s.onTrackUnpublished
	s.room.Callback.OnDisconnected = s.onComplete

	logger.Debugw("connecting to room")
	if err := s.room.JoinWithToken(p.LKUrl, p.Token); err != nil {
		return nil, err
	}

	if err := s.subscribeToTracks(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *SDKSource) subscribeToTracks() error {
	expecting := make(map[string]bool)
	for _, trackID := range s.trackIDs {
		expecting[trackID] = true
	}

	for _, p := range s.room.GetParticipants() {
		for _, track := range p.Tracks() {
			if expecting[track.SID()] {
				if rt, ok := track.(*lksdk.RemoteTrackPublication); ok {
					err := rt.SetSubscribed(true)
					if err != nil {
						return err
					}

					delete(expecting, track.SID())
					s.active.Inc()
					if len(expecting) == 0 {
						return nil
					}
				}
			}
		}
	}

	for trackID := range expecting {
		return errors.ErrTrackNotFound(trackID)
	}

	return nil
}

func (s *SDKSource) onTrackUnpublished(track *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
	for _, trackID := range s.trackIDs {
		if track.SID() == trackID {
			if s.active.Dec() == 0 {
				s.onComplete()
			}
			return
		}
	}
}

func (s *SDKSource) onComplete() {
	select {
	case <-s.endRecording:
		return
	default:
		close(s.endRecording)
	}
}

func (s *SDKSource) EndRecording() chan struct{} {
	return s.endRecording
}

func (s *SDKSource) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, writer := range s.writers {
		writer.stop()
	}
	s.room.Disconnect()
}

type trackWriter struct {
	sb     *samplebuilder.SampleBuilder
	writer media.Writer
	track  *webrtc.TrackRemote
	closed chan struct{}
}

func (t *trackWriter) start() {
	defer func() {
		err := t.writer.Close()
		if err != nil {
			logger.Errorw("could not close track writer", err)
		}
	}()
	for {
		select {
		case <-t.closed:
			return
		default:
			pkt, _, err := t.track.ReadRTP()
			if err != nil {
				logger.Errorw("could not read from track", err)
				return
			}
			t.sb.Push(pkt)

			for _, p := range t.sb.PopPackets() {
				if err = t.writer.WriteRTP(p); err != nil {
					logger.Errorw("could not write to file", err)
					return
				}
			}
		}
	}
}

func (t *trackWriter) stop() {
	select {
	case <-t.closed:
		return
	default:
		close(t.closed)
	}
}
