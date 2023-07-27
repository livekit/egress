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
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

type PlaylistWriter struct {
	filename       string
	targetDuration int
}

func NewPlaylistWriter(filename string, targetDuration int) (*PlaylistWriter, error) {
	p := &PlaylistWriter{
		filename:       filename,
		targetDuration: targetDuration,
	}

	f, err := os.Create(p.filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	sb.WriteString("#EXT-X-VERSION:4\n")
	sb.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n")
	sb.WriteString("#EXT-X-ALLOW-CACHE:NO\n")
	sb.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	sb.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", p.targetDuration))

	_, err = f.WriteString(sb.String())
	if err != nil {
		return nil, err
	}

	return p, nil
}

func (p *PlaylistWriter) Append(dateTime time.Time, duration float64, filename string) error {
	f, err := os.OpenFile(p.filename, os.O_WRONLY|os.O_APPEND, fs.ModeAppend)
	if err != nil {
		return err
	}
	defer f.Close()

	var sb strings.Builder
	sb.WriteString("#EXT-X-PROGRAM-DATE-TIME:")
	sb.WriteString(dateTime.UTC().Format("2006-01-02T15:04:05.999Z07:00"))
	sb.WriteString("\n#EXTINF:")
	sb.WriteString(strconv.FormatFloat(duration, 'f', 3, 32))
	sb.WriteString(",\n")
	sb.WriteString(filename)
	sb.WriteString("\n")

	_, err = f.WriteString(sb.String())
	return err
}

// Close sliding playlist and make them fixed.
func (p *PlaylistWriter) Close() error {
	f, err := os.OpenFile(p.filename, os.O_WRONLY|os.O_APPEND, fs.ModeAppend)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString("#EXT-X-ENDLIST\n")
	return err
}
