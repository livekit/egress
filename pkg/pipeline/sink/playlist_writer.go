package sink

import (
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/grafov/m3u8"
	"github.com/livekit/egress/pkg/pipeline/params"
)

type PlaylistWriter struct {
	playlist                  *m3u8.MediaPlaylist
	currentItemStartTimestamp int64
	currentItemFilename       string
	playlistPath              string
}

func NewPlaylistWriter(p *params.Params) (*PlaylistWriter, error) {

	dir, _ := path.Split(p.FilePrefix)
	playlistPath := path.Join(dir, p.PlaylistFilename)

	// "github.com/grafov/m3u8" is fairly inefficient for frequent serializations of long playlists and
	// doesn't implement recent additions to the HLS spec, but I'm not aware of anything better, short of
	// writing one.
	playlist, err := m3u8.NewMediaPlaylist(0, 15000) // 15,000 -> about 24h with 6s segments
	if err != nil {
		return nil, err
	}

	playlist.MediaType = m3u8.EVENT
	playlist.SetVersion(4) // Needed because we have float segment durations

	return &PlaylistWriter{
		playlist:                  playlist,
		playlistPath:              playlistPath,
		currentItemStartTimestamp: -1,
	}, nil
}

func (w *PlaylistWriter) StartSegment(filepath string, startTime int64) error {
	if startTime < 0 {
		return fmt.Errorf("Invalid Start Timestamp")
	}

	if filepath == "" {
		return fmt.Errorf("Invalid Filename")
	}

	w.currentItemStartTimestamp = startTime
	_, w.currentItemFilename = path.Split(filepath)

	return nil
}

func (w *PlaylistWriter) EndSegment(endTime int64) error {
	if endTime <= w.currentItemStartTimestamp {
		return fmt.Errorf("Segment end time before start time")
	}

	duration := float64(endTime-w.currentItemStartTimestamp) / float64(time.Second)

	err := w.finalizeSegment(duration)
	if err != nil {
		return err
	}

	// Write playlist for every segment. This allows better crash recovery and to use
	// it as an Event playlist, at the cost of extra I/O
	w.writePlaylist()

	return nil
}

func (w *PlaylistWriter) EOS() error {
	if w.segmentPending() {
		// We do not have the segment end time. Use target duration instead
		err := w.finalizeSegment(w.playlist.TargetDuration)
		if err != nil {
			return err
		}
	}

	w.playlist.Close()

	err := w.writePlaylist()
	if err != nil {
		return err
	}

	return nil
}

func (w *PlaylistWriter) segmentPending() bool {
	return w.currentItemFilename != "" && w.currentItemStartTimestamp >= 0
}

func (w *PlaylistWriter) finalizeSegment(duration float64) error {
	if !w.segmentPending() {
		return fmt.Errorf("No pending Segment")
	}

	w.playlist.Append(w.currentItemFilename, duration, "")

	w.currentItemFilename = ""
	w.currentItemStartTimestamp = -1

	return nil
}

func (w *PlaylistWriter) writePlaylist() error {
	buf := w.playlist.Encode()

	f, err := os.Create(w.playlistPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	_, err = io.Copy(f, buf)
	if err != nil {
		return err
	}

	return nil
}
