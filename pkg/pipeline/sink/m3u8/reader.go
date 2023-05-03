package m3u8

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Playlist struct {
	Version        int
	MediaType      string
	TargetDuration int
	Segments       []*Segment
	Closed         bool
}

type Segment struct {
	ProgramDateTime time.Time
	Duration        float64
	Filename        string
}

func ReadPlaylist(filename string) (*Playlist, error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(b), "\n")
	version, _ := strconv.Atoi(strings.Split(lines[1], ":")[1])
	mediaType := strings.Split(lines[2], ":")[1]
	targetDuration, _ := strconv.Atoi(strings.Split(lines[5], ":")[1])

	p := &Playlist{
		Version:        version,
		MediaType:      mediaType,
		TargetDuration: targetDuration,
		Segments:       make([]*Segment, 0),
	}

	for i := 6; i < len(lines)-3; i += 3 {
		startTime, _ := time.Parse(time.RFC3339Nano, strings.SplitN(lines[i], ":", 2)[1])
		duration, _ := strconv.ParseFloat(strings.Split(lines[i+1], ":")[1], 64)

		p.Segments = append(p.Segments, &Segment{
			ProgramDateTime: startTime,
			Duration:        duration,
			Filename:        lines[i+2],
		})
	}

	if lines[len(lines)-2] == "#EXT-X-ENDLIST" {
		p.Closed = true
	}

	return p, nil
}

func (p *Playlist) Count() int {
	return len(p.Segments)
}
