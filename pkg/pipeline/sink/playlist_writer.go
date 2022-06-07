package sink

import (
	"fmt"
	"io"
	"os"
	"path"
	"sync"
	"time"

	"github.com/grafov/m3u8"
	"github.com/livekit/egress/pkg/pipeline/params"
)

type PlaylistWriter struct {
	playlist                  *m3u8.MediaPlaylist
	currentItemStartTimestamp int64
	currentItemFilename       string
	playlistPath              string

	openSegmentsStartTime map[string]int64
	openSegmentsLock      sync.Mutex
}

func NewPlaylistWriter(p *params.Params) (*PlaylistWriter, error) {

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
		playlist:              playlist,
		playlistPath:          p.PlaylistFilename,
		openSegmentsStartTime: make(map[string]int64),
	}, nil
}

func (w *PlaylistWriter) StartSegment(filepath string, startTime int64) error {
	if filepath == "" {
		return fmt.Errorf("Invalid Filename")
	}

	if startTime < 0 {
		return fmt.Errorf("Invalid Start Timestamp")
	}

	k := getFilenameFromFilePath(filepath)

	w.openSegmentsLock.Lock()
	defer w.openSegmentsLock.Unlock()
	if _, ok := w.openSegmentsStartTime[k]; ok {
		return fmt.Errorf("Segment with this name already started")
	}

	w.openSegmentsStartTime[k] = startTime

	return nil
}

func (w *PlaylistWriter) EndSegment(filepath string, endTime int64) error {
	if filepath == "" {
		return fmt.Errorf("Invalid Filename")
	}

	if endTime <= w.currentItemStartTimestamp {
		return fmt.Errorf("Segment end time before start time")
	}

	k := getFilenameFromFilePath(filepath)

	w.openSegmentsLock.Lock()
	defer w.openSegmentsLock.Unlock()

	t, ok := w.openSegmentsStartTime[k]
	if !ok {
		return fmt.Errorf("No open segment with the name %s", k)
	}
	delete(w.openSegmentsStartTime, k)

	duration := float64(endTime-t) / float64(time.Second)

	// This assumes EndSegment will be called in the same order as StartSegment
	err := w.playlist.Append(k, duration, "")
	if err != nil {
		return err
	}

	// Write playlist for every segment. This allows better crash recovery and to use
	// it as an Event playlist, at the cost of extra I/O
	w.writePlaylist()

	return nil
}

func (w *PlaylistWriter) EOS() error {
	w.playlist.Close()

	err := w.writePlaylist()
	if err != nil {
		return err
	}

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

func getFilenameFromFilePath(filepath string) string {
	_, filename := path.Split(filepath)

	return filename
}
