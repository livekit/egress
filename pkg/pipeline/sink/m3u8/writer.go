// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package m3u8

import (
	"container/list"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

type PlaylistType string

const (
	PlaylistTypeLive  PlaylistType = ""
	PlaylistTypeEvent PlaylistType = "EVENT"
)

const (
	playlistVersion = 4

	// fmp4 segments need an EXT-X-MAP, which a playlist without EXT-X-I-FRAMES-ONLY
	// may only carry from version 6 on, and fmp4 itself is a version 7 feature
	fmp4PlaylistVersion = 7
)

type PlaylistWriter interface {
	Append(dateTime time.Time, duration float64, filename string) error
	Close() error
}

type basePlaylistWriter struct {
	filename       string
	targetDuration int
	initSegment    string
}

type eventPlaylistWriter struct {
	basePlaylistWriter
}

type livePlaylistWriter struct {
	basePlaylistWriter

	windowSize int
	mediaSeq   int

	livePlaylistHeader   string
	livePlaylistSegments *list.List
}

func (p *basePlaylistWriter) createHeader(plType PlaylistType) string {
	version := playlistVersion
	if p.initSegment != "" {
		version = fmp4PlaylistVersion
	}

	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	fmt.Fprintf(&sb, "#EXT-X-VERSION:%d\n", version)
	if plType != PlaylistTypeLive {
		fmt.Fprintf(&sb, "#EXT-X-PLAYLIST-TYPE:%s\n", plType)
	}
	// EXT-X-ALLOW-CACHE was removed in version 7
	if version < fmp4PlaylistVersion {
		sb.WriteString("#EXT-X-ALLOW-CACHE:NO\n")
	}
	fmt.Fprintf(&sb, "#EXT-X-TARGETDURATION:%d\n", p.targetDuration)
	if plType != PlaylistTypeLive {
		sb.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	}
	if p.initSegment != "" {
		fmt.Fprintf(&sb, "#EXT-X-MAP:URI=%q\n", p.initSegment)
	}

	return sb.String()
}

func (p *basePlaylistWriter) createSegmentEntry(dateTime time.Time, duration float64, filename string) string {
	var sb strings.Builder

	sb.WriteString("#EXT-X-PROGRAM-DATE-TIME:")
	sb.WriteString(dateTime.UTC().Format("2006-01-02T15:04:05.999Z07:00"))
	sb.WriteString("\n#EXTINF:")
	sb.WriteString(strconv.FormatFloat(duration, 'f', 3, 32))
	sb.WriteString(",\n")
	sb.WriteString(filename)
	sb.WriteString("\n")

	return sb.String()
}

// NewEventPlaylistWriter creates an event playlist. initSegment names the
// initialization segment for fmp4 output, and is empty for self-contained segments.
func NewEventPlaylistWriter(filename string, targetDuration int, initSegment string) (PlaylistWriter, error) {
	p := &eventPlaylistWriter{
		basePlaylistWriter: basePlaylistWriter{
			filename:       filename,
			targetDuration: targetDuration,
			initSegment:    initSegment,
		},
	}

	f, err := os.Create(p.filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	_, err = f.WriteString(p.createHeader(PlaylistTypeEvent))
	if err != nil {
		return nil, err
	}

	return p, nil
}

func (p *eventPlaylistWriter) Append(dateTime time.Time, duration float64, filename string) error {
	f, err := os.OpenFile(p.filename, os.O_WRONLY|os.O_APPEND, fs.ModeAppend)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(p.createSegmentEntry(dateTime, duration, filename))
	return err
}

// Close sliding playlist and make them fixed.
func (p *eventPlaylistWriter) Close() error {
	f, err := os.OpenFile(p.filename, os.O_WRONLY|os.O_APPEND, fs.ModeAppend)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString("#EXT-X-ENDLIST\n")
	return err
}

// NewLivePlaylistWriter creates a sliding window playlist. initSegment names the
// initialization segment for fmp4 output, and is empty for self-contained segments.
func NewLivePlaylistWriter(filename string, targetDuration int, windowSize int, initSegment string) (PlaylistWriter, error) {
	p := &livePlaylistWriter{
		basePlaylistWriter: basePlaylistWriter{
			filename:       filename,
			targetDuration: targetDuration,
			initSegment:    initSegment,
		},
		windowSize:           windowSize,
		livePlaylistSegments: list.New(),
	}

	p.livePlaylistHeader = p.createHeader(PlaylistTypeLive)

	return p, nil
}

func (p *livePlaylistWriter) Append(dateTime time.Time, duration float64, filename string) error {
	f, err := os.Create(p.filename)
	if err != nil {
		return err
	}
	defer f.Close()

	segmentStr := p.createSegmentEntry(dateTime, duration, filename)
	p.livePlaylistSegments.PushBack(segmentStr)

	for p.livePlaylistSegments.Len() > p.windowSize {
		p.livePlaylistSegments.Remove(p.livePlaylistSegments.Front())
		p.mediaSeq++
	}

	_, err = f.WriteString(p.generatePlaylist())
	return err
}

func (p *livePlaylistWriter) Close() error {
	f, err := os.Create(p.filename)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(p.generatePlaylist())
	if err != nil {
		return err
	}

	_, err = f.WriteString("#EXT-X-ENDLIST\n")
	return err
}

func (p *livePlaylistWriter) generatePlaylist() string {
	var sb strings.Builder
	sb.WriteString(p.livePlaylistHeader)
	fmt.Fprintf(&sb, "#EXT-X-MEDIA-SEQUENCE:%d\n", p.mediaSeq)
	for elem := p.livePlaylistSegments.Front(); elem != nil; elem = elem.Next() {
		segmentStr := elem.Value.(string)
		sb.WriteString(segmentStr)
	}

	return sb.String()
}
