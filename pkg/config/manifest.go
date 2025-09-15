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

package config

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/linkdata/deadlock"
)

type Manifest struct {
	EgressID          string `json:"egress_id,omitempty"`
	RoomID            string `json:"room_id,omitempty"`
	RoomName          string `json:"room_name,omitempty"`
	Url               string `json:"url,omitempty"`
	StartedAt         int64  `json:"started_at,omitempty"`
	EndedAt           int64  `json:"ended_at,omitempty"`
	PublisherIdentity string `json:"publisher_identity,omitempty"`
	TrackID           string `json:"track_id,omitempty"`
	TrackKind         string `json:"track_kind,omitempty"`
	TrackSource       string `json:"track_source,omitempty"`
	AudioTrackID      string `json:"audio_track_id,omitempty"`
	VideoTrackID      string `json:"video_track_id,omitempty"`

	mu        deadlock.Mutex
	Files     []*File     `json:"files,omitempty"`
	Playlists []*Playlist `json:"playlists,omitempty"`
	Images    []*Image    `json:"images,omitempty"`
}

type File struct {
	Filename string `json:"filename,omitempty"`
	Location string `json:"location,omitempty"`
}

type Playlist struct {
	mu       deadlock.Mutex
	Location string     `json:"location,omitempty"`
	Segments []*Segment `json:"segments,omitempty"`
}

type Segment struct {
	Filename string `json:"filename,omitempty"`
	Location string `json:"location,omitempty"`
}

type Image struct {
	Filename  string    `json:"filename,omitempty"`
	Timestamp time.Time `json:"timestamp,omitempty"`
	Location  string    `json:"location,omitempty"`
}

func (p *PipelineConfig) initManifest() {
	if p.shouldCreateManifest() {
		p.Manifest = &Manifest{
			EgressID:          p.Info.EgressId,
			RoomID:            p.Info.RoomId,
			RoomName:          p.Info.RoomName,
			Url:               p.WebUrl,
			StartedAt:         p.Info.StartedAt,
			PublisherIdentity: p.Identity,
			TrackID:           p.TrackID,
			TrackKind:         p.TrackKind,
			TrackSource:       p.TrackSource,
			AudioTrackID:      p.AudioTrackID,
			VideoTrackID:      p.VideoTrackID,
		}
	}
}

func (p *PipelineConfig) shouldCreateManifest() bool {
	if p.BackupConfig != nil {
		return true
	}
	if fc := p.GetFileConfig(); fc != nil && !fc.DisableManifest {
		return true
	}
	if sc := p.GetSegmentConfig(); sc != nil && !sc.DisableManifest {
		return true
	}
	for _, ic := range p.GetImageConfigs() {
		if !ic.DisableManifest {
			return true
		}
	}
	return false
}

func (m *Manifest) AddFile(filename, location string) {
	m.mu.Lock()
	m.Files = append(m.Files, &File{
		Filename: filename,
		Location: location,
	})
	m.mu.Unlock()
}

func (m *Manifest) AddPlaylist() *Playlist {
	p := &Playlist{}

	m.mu.Lock()
	m.Playlists = append(m.Playlists, p)
	m.mu.Unlock()

	return p
}

func (p *Playlist) UpdateLocation(location string) {
	p.mu.Lock()
	p.Location = location
	p.mu.Unlock()
}

func (p *Playlist) AddSegment(filename, location string) {
	p.mu.Lock()
	p.Segments = append(p.Segments, &Segment{
		Filename: filename,
		Location: location,
	})
	p.mu.Unlock()
}

func (m *Manifest) AddImage(filename string, ts time.Time, location string) {
	m.mu.Lock()
	m.Images = append(m.Images, &Image{
		Filename:  filename,
		Timestamp: ts,
		Location:  location,
	})
	m.mu.Unlock()
}

func (m *Manifest) Close(endedAt int64) ([]byte, error) {
	m.EndedAt = endedAt

	buf := bytes.NewBuffer(nil)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
